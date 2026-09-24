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
	"github.com/ccfos/huatuo/internal/memsnapshot"
	goprovider "github.com/ccfos/huatuo/internal/memsnapshot/providers/golang"
	"github.com/ccfos/huatuo/internal/memsnapshot/providers/java"
	"github.com/ccfos/huatuo/internal/memsnapshot/providers/python"
)

// Options uses the before-OOM budgets as defaults for zero-valued fields.
// Timeouts are cooperative: they cannot interrupt an in-flight syscall.
type Options struct {
	TopK             int
	DetectionTimeout time.Duration
	CaptureTimeout   time.Duration

	// CheckTarget adds caller-specific checks before detection, dispatch and
	// returning the result. It must not persist data or mutate the target.
	// Identity checks remain owned by Capture.
	CheckTarget func(context.Context, memsnapshot.ProcessIdentity) error
}

// Result carries runtime data without event or container metadata.
type Result struct {
	Identity      memsnapshot.ProcessIdentity
	Language      memsnapshot.Language
	CaptureTime   time.Time
	SamplingSeed  uint64
	Snapshot      *memsnapshot.Snapshot
	ProcessMemory *memsnapshot.ProcessMemory
}

// Capture binds memory collection to an already selected process identity.
// Detection/provider failures become failed snapshots. Invalid targets,
// cancellation and invalid options return errors without a result.
func Capture(ctx context.Context, identity memsnapshot.ProcessIdentity, options Options) (*Result, error) {
	return capture(ctx, identity, options, memsnapshot.DetectLanguage, newProvider)
}

func capture(ctx context.Context, identity memsnapshot.ProcessIdentity, options Options,
	detect func(context.Context, int) (memsnapshot.Language, error),
	provider func(memsnapshot.Language) memsnapshot.Provider,
) (result *Result, retErr error) {
	pid := identity.TGID
	started := time.Now()
	stage := "validate_target"
	log.WithField("pid", pid).
		WithField("capture_id", started.UnixNano()).
		WithField("stage", stage).
		Info("memsnapshot started")
	defer func() {
		log.WithField("pid", pid).
			WithField("capture_id", started.UnixNano()).
			WithField("stage", stage).
			WithField("elapsed_ms", time.Since(started).Milliseconds()).
			WithError(retErr).
			Info("memsnapshot finished")
	}()
	if err := options.defaults(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	validate := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := memsnapshot.ValidateIdentity("/proc", identity); err != nil {
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
	log.WithField("pid", pid).
		WithField("capture_id", started.UnixNano()).
		Info("memsnapshot process memory started")
	processMemory := readProcessMemory(pid)
	log.WithField("pid", pid).
		WithField("capture_id", started.UnixNano()).
		WithField("elapsed_ms", time.Since(phaseStarted).Milliseconds()).
		WithField("status", processMemory.Status).
		WithField("reason", processMemory.Reason).
		Info("memsnapshot process memory finished")
	stage = "detect_language"
	phaseStarted = time.Now()
	log.WithField("pid", pid).
		WithField("capture_id", started.UnixNano()).
		WithField("timeout_ms", options.DetectionTimeout.Milliseconds()).
		Info("memsnapshot language detection started")
	detectionCtx, cancelDetection := context.WithTimeout(ctx, options.DetectionTimeout)
	language, detectionErr := detect(detectionCtx, pid)
	if err := detectionCtx.Err(); err != nil {
		detectionErr = err
	}
	cancelDetection()
	log.WithField("pid", pid).
		WithField("capture_id", started.UnixNano()).
		WithField("elapsed_ms", time.Since(phaseStarted).Milliseconds()).
		WithField("language", language).
		WithError(detectionErr).
		Info("memsnapshot language detection finished")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seed := uint64(now.UnixNano())
	if seed == 0 {
		seed = 1
	}
	var snapshot *memsnapshot.Snapshot
	if detectionErr != nil {
		snapshot = memsnapshot.Failed("detect victim runtime: " + detectionErr.Error())
	} else {
		// Detection itself may race migration/reuse; check again at dispatch.
		stage = "validate_before_capture"
		if err := validate(); err != nil {
			return nil, fmt.Errorf("validate process before capture: %w", err)
		}
		captureCtx, cancelCapture := context.WithTimeout(ctx, options.CaptureTimeout)
		captureStarted := time.Now()
		stage = "runtime_capture"
		log.WithField("pid", pid).
			WithField("capture_id", started.UnixNano()).
			WithField("language", language).
			WithField("timeout_ms", options.CaptureTimeout.Milliseconds()).
			WithField("top_k", options.TopK).
			Info("memsnapshot runtime capture started")
		var err error
		snapshot, err = captureProvider(captureCtx, provider(language), memsnapshot.Request{
			SamplingSeed: seed, Identity: identity, TopK: options.TopK,
		})
		if err != nil {
			snapshot = memsnapshot.Failed(err.Error())
		}
		snapshot.DurationMS = uint64((time.Since(captureStarted) + time.Millisecond - 1) / time.Millisecond)
		if err := captureCtx.Err(); err != nil {
			snapshot = memsnapshot.Failed("capture victim runtime: " + err.Error())
		}
		cancelCapture()
		log.WithField("pid", pid).
			WithField("capture_id", started.UnixNano()).
			WithField("language", language).
			WithField("elapsed_ms", time.Since(captureStarted).Milliseconds()).
			WithField("status", snapshot.Status).
			WithField("reason", snapshot.Reason).
			Info("memsnapshot runtime capture finished")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snapshot.DurationMS == 0 {
		snapshot.DurationMS = uint64((time.Since(captureStarted) + time.Millisecond - 1) / time.Millisecond)
	}
	stage = "limit_output"
	if err := memsnapshot.LimitOutput(snapshot, options.TopK); err != nil {
		durationMS := snapshot.DurationMS
		snapshot = memsnapshot.Failed("limit runtime capture output: " + err.Error())
		snapshot.DurationMS = durationMS
		if err := memsnapshot.LimitOutput(snapshot, options.TopK); err != nil {
			return nil, err
		}
	}
	stage = "validate_before_return"
	if err := validate(); err != nil {
		return nil, fmt.Errorf("validate process before returning snapshot: %w", err)
	}
	result = &Result{
		Identity: identity, Language: language, CaptureTime: now,
		SamplingSeed: seed, Snapshot: snapshot, ProcessMemory: processMemory,
	}
	stage = "done"
	return result, nil
}

func readProcessMemory(pid int) *memsnapshot.ProcessMemory {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return &memsnapshot.ProcessMemory{Status: memsnapshot.StatusUnavailable, Reason: err.Error()}
	}
	defer f.Close()
	return parseProcessMemory(f)
}

func parseProcessMemory(r io.Reader) *memsnapshot.ProcessMemory {
	m := &memsnapshot.ProcessMemory{Status: memsnapshot.StatusComplete}
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
		m.Status, m.Reason = memsnapshot.StatusPartial, "process memory fields are missing or invalid"
		if available == 0 {
			m.Status = memsnapshot.StatusUnavailable
		}
	}
	if err := scanner.Err(); err != nil {
		m.Status, m.Reason = memsnapshot.StatusPartial, "read process status: "+err.Error()
		if available == 0 {
			m.Status = memsnapshot.StatusUnavailable
		}
	}
	return m
}

func (o *Options) defaults() error {
	if o.TopK == 0 {
		o.TopK = 10
	}
	if o.TopK < 1 || o.TopK > memsnapshot.MaxMemoryObjectEntries {
		return fmt.Errorf("snapshot top-K must be in [1, %d], got %d", memsnapshot.MaxMemoryObjectEntries, o.TopK)
	}
	for _, budget := range []struct {
		name     string
		value    *time.Duration
		fallback time.Duration
	}{
		{"detection", &o.DetectionTimeout, time.Second},
		{"capture", &o.CaptureTimeout, 2 * time.Second},
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

func newProvider(language memsnapshot.Language) memsnapshot.Provider {
	switch language {
	case memsnapshot.LanguageGo:
		return goprovider.New()
	case memsnapshot.LanguageJava:
		return java.New()
	case memsnapshot.LanguagePython:
		return python.New()
	default:
		return nil
	}
}

// Capture remains synchronous: cancellation stops subsequent work but cannot
// preempt an in-flight syscall. Recover provider panics at the collector boundary.
func captureProvider(ctx context.Context, provider memsnapshot.Provider,
	request memsnapshot.Request,
) (snapshot *memsnapshot.Snapshot, err error) {
	if provider == nil {
		return memsnapshot.Unavailable("runtime is not supported"), nil
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
