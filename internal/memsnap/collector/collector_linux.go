// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package collector orchestrates runtime snapshots independently of their trigger.
package collector

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/memsnap"
)

// Options uses the before-OOM budgets as defaults for zero-valued fields.
// Timeouts are cooperative: they cannot interrupt an in-flight syscall.
type Options struct {
	TopK             int
	DetectionTimeout time.Duration
	GoTimeout        time.Duration
	JavaTimeout      time.Duration
	PythonTimeout    time.Duration

	// ExpectedIdentity binds collection to a process selected earlier.
	ExpectedIdentity *memsnap.ProcessIdentity
	// CheckTarget adds caller-specific checks before detection, dispatch and
	// returning/saving the result. Identity checks remain owned by Run.
	CheckTarget func(context.Context, memsnap.ProcessIdentity) error
	// Save is optional and synchronous. It receives the bounded result only
	// after final target validation; storage metadata belongs to the caller.
	Save func(context.Context, *Result) error
}

// Result carries runtime data without event or container metadata.
type Result struct {
	Identity      memsnap.ProcessIdentity
	Language      memsnap.Language
	CaptureTime   time.Time
	SamplingSeed  uint64
	Snapshot      *memsnap.Snapshot
	ProcessMemory *memsnap.ProcessMemory
}

// Run identifies and captures pid, then optionally saves the result.
// Detection/provider failures become failed snapshots. Invalid targets,
// cancellation, invalid options and persistence failures return errors.
func Run(ctx context.Context, pid int, options Options) (*Result, error) {
	return run(ctx, pid, options, memsnap.DetectLanguage, newProvider)
}

func run(ctx context.Context, pid int, options Options,
	detect func(context.Context, int) (memsnap.Language, error),
	provider func(memsnap.Language) memsnap.Provider,
) (result *Result, retErr error) {
	started := time.Now()
	stage := "validate_target"
	log.Infof("memsnap started: pid=%d capture_id=%d stage=%s", pid, started.UnixNano(), stage)
	defer func() {
		log.Infof("memsnap finished: pid=%d capture_id=%d stage=%s elapsed=%s error=%v",
			pid, started.UnixNano(), stage, time.Since(started), retErr)
	}()
	if err := options.defaults(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := memsnap.ReadIdentity(pid)
	if err != nil {
		return nil, err
	}
	if options.ExpectedIdentity != nil && identity != *options.ExpectedIdentity {
		return nil, errors.New("selected process identity changed")
	}
	validate := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := memsnap.ValidateIdentity("/proc", identity); err != nil {
			return err
		}
		if options.CheckTarget != nil {
			checkCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if err := options.CheckTarget(checkCtx, identity); err != nil {
				return err
			}
			if err := checkCtx.Err(); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	if err := validate(); err != nil {
		return nil, fmt.Errorf("validate selected process: %w", err)
	}

	captureStarted := time.Now()
	now := captureStarted.UTC()
	// Capture before runtime detection so unsupported runtimes retain diagnostics.
	stage = "process_memory"
	phaseStarted := time.Now()
	log.Infof("memsnap process memory started: pid=%d capture_id=%d", pid, started.UnixNano())
	processMemory := readProcessMemory(pid)
	log.Infof("memsnap process memory finished: pid=%d capture_id=%d elapsed=%s status=%s reason=%q",
		pid, started.UnixNano(), time.Since(phaseStarted), processMemory.Status, processMemory.Reason)
	stage = "detect_language"
	phaseStarted = time.Now()
	log.Infof("memsnap language detection started: pid=%d capture_id=%d timeout=%s", pid, started.UnixNano(), options.DetectionTimeout)
	detectionCtx, cancelDetection := context.WithTimeout(ctx, options.DetectionTimeout)
	language, detectionErr := detect(detectionCtx, pid)
	if err := detectionCtx.Err(); err != nil {
		detectionErr = err
	}
	cancelDetection()
	log.Infof("memsnap language detection finished: pid=%d capture_id=%d elapsed=%s language=%s error=%v",
		pid, started.UnixNano(), time.Since(phaseStarted), language, detectionErr)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seed := uint64(now.UnixNano())
	if seed == 0 {
		seed = 1
	}
	var snapshot *memsnap.Snapshot
	if detectionErr != nil {
		snapshot = memsnap.Failed("detect victim runtime: " + detectionErr.Error())
	} else {
		// Detection itself may race migration/reuse; check again at dispatch.
		stage = "validate_before_capture"
		if err := validate(); err != nil {
			return nil, fmt.Errorf("validate process before capture: %w", err)
		}
		captureCtx, cancelCapture := context.WithTimeout(ctx, options.captureTimeout(language))
		captureStarted := time.Now()
		stage = "runtime_capture"
		log.Infof("memsnap runtime capture started: pid=%d capture_id=%d language=%s timeout=%s top_k=%d",
			pid, started.UnixNano(), language, options.captureTimeout(language), options.TopK)
		snapshot, err = captureProvider(captureCtx, provider(language), memsnap.Request{
			SamplingSeed: seed, Identity: identity, TopK: options.TopK,
		})
		if err != nil {
			snapshot = memsnap.Failed(err.Error())
		}
		snapshot.DurationMS = uint64((time.Since(captureStarted) + time.Millisecond - 1) / time.Millisecond)
		if err := captureCtx.Err(); err != nil {
			snapshot = memsnap.Failed("capture victim runtime: " + err.Error())
		}
		cancelCapture()
		log.Infof("memsnap runtime capture finished: pid=%d capture_id=%d language=%s elapsed=%s status=%s reason=%q",
			pid, started.UnixNano(), language, time.Since(captureStarted), snapshot.Status, snapshot.Reason)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snapshot.DurationMS == 0 {
		snapshot.DurationMS = uint64((time.Since(captureStarted) + time.Millisecond - 1) / time.Millisecond)
	}
	stage = "limit_output"
	if err := memsnap.LimitOutput(snapshot, options.TopK); err != nil {
		durationMS := snapshot.DurationMS
		snapshot = memsnap.Failed("limit runtime capture output: " + err.Error())
		snapshot.DurationMS = durationMS
		if err := memsnap.LimitOutput(snapshot, options.TopK); err != nil {
			return nil, err
		}
	}
	stage = "validate_before_save"
	if err := validate(); err != nil {
		return nil, fmt.Errorf("validate process before persistence: %w", err)
	}
	result = &Result{
		Identity: identity, Language: language, CaptureTime: now,
		SamplingSeed: seed, Snapshot: snapshot, ProcessMemory: processMemory,
	}
	if options.Save != nil {
		stage = "save"
		phaseStarted = time.Now()
		log.Infof("memsnap save started: pid=%d capture_id=%d snapshot_status=%s", pid, started.UnixNano(), snapshot.Status)
		err := options.Save(ctx, result)
		log.Infof("memsnap save finished: pid=%d capture_id=%d elapsed=%s error=%v", pid, started.UnixNano(), time.Since(phaseStarted), err)
		if err != nil {
			return result, err
		}
	}
	stage = "done"
	return result, nil
}

func readProcessMemory(pid int) *memsnap.ProcessMemory {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return &memsnap.ProcessMemory{Status: memsnap.StatusUnavailable, Reason: err.Error()}
	}
	defer f.Close()
	return parseProcessMemory(f)
}

func parseProcessMemory(r io.Reader) *memsnap.ProcessMemory {
	m := &memsnap.ProcessMemory{Status: memsnap.StatusComplete}
	fields := map[string]**uint64{
		"VmSize:": &m.VirtualBytes, "VmRSS:": &m.RSSBytes,
		"RssAnon:": &m.RSSAnonBytes, "RssFile:": &m.RSSFileBytes,
		"RssShmem:": &m.RSSShmemBytes, "VmSwap:": &m.SwapBytes,
		"VmPTE:": &m.PageTableBytes,
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 0 {
			continue
		}
		dst, ok := fields[parts[0]]
		if !ok || len(parts) != 3 || parts[2] != "kB" {
			continue
		}
		value, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil || value > ^uint64(0)/1024 {
			continue
		}
		value *= 1024
		*dst = &value
	}
	available := 0
	for _, value := range fields {
		if *value != nil {
			available++
		}
	}
	if available != len(fields) {
		m.Status, m.Reason = memsnap.StatusPartial, "process memory fields are missing or invalid"
		if available == 0 {
			m.Status = memsnap.StatusUnavailable
		}
	}
	if err := scanner.Err(); err != nil {
		m.Status, m.Reason = memsnap.StatusPartial, "read process status: "+err.Error()
		if available == 0 {
			m.Status = memsnap.StatusUnavailable
		}
	}
	return m
}

func (o *Options) defaults() error {
	if o.TopK == 0 {
		o.TopK = 10
	}
	if o.TopK < 1 || o.TopK > memsnap.MaxTopK {
		return fmt.Errorf("snapshot top-K must be in [1, %d], got %d", memsnap.MaxTopK, o.TopK)
	}
	for _, budget := range []struct {
		name     string
		value    *time.Duration
		fallback time.Duration
	}{
		{"detection", &o.DetectionTimeout, time.Second},
		{"Go", &o.GoTimeout, 100 * time.Millisecond},
		{"Java", &o.JavaTimeout, 2 * time.Second},
		{"Python", &o.PythonTimeout, 2 * time.Second},
	} {
		if *budget.value < 0 {
			return fmt.Errorf("%s timeout must not be negative", budget.name)
		}
		if *budget.value == 0 {
			*budget.value = budget.fallback
		}
	}
	return nil
}

func (o *Options) captureTimeout(language memsnap.Language) time.Duration {
	switch language {
	case memsnap.LanguageJava:
		return o.JavaTimeout
	case memsnap.LanguagePython:
		return o.PythonTimeout
	default:
		return o.GoTimeout
	}
}

func newProvider(_ memsnap.Language) memsnap.Provider {
	return nil
}

// Capture remains synchronous: cancellation stops subsequent work but cannot
// preempt an in-flight syscall. Recover provider panics at the collector boundary.
func captureProvider(ctx context.Context, provider memsnap.Provider,
	request memsnap.Request,
) (snapshot *memsnap.Snapshot, err error) {
	if provider == nil {
		return memsnap.Unavailable("runtime is not supported"), nil
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			snapshot = nil
			err = fmt.Errorf("runtime capture panic: %v", recovered)
		}
	}()

	snapshot, err = provider.Capture(ctx, request)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, errors.New("runtime capture returned no snapshot")
	}
	return snapshot, nil
}
