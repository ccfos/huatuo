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
	"context"
	"errors"
	"fmt"
	"time"

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
	Identity     memsnap.ProcessIdentity
	Language     memsnap.Language
	CaptureTime  time.Time
	SamplingSeed uint64
	Snapshot     *memsnap.Snapshot
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
) (*Result, error) {
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

	started := time.Now()
	detectionCtx, cancelDetection := context.WithTimeout(ctx, options.DetectionTimeout)
	language, detectionErr := detect(detectionCtx, pid)
	if err := detectionCtx.Err(); err != nil {
		detectionErr = err
	}
	cancelDetection()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	seed := uint64(now.UnixNano())
	if seed == 0 {
		seed = 1
	}
	var snapshot *memsnap.Snapshot
	if detectionErr != nil {
		snapshot = memsnap.Failed("detect victim runtime: " + detectionErr.Error())
	} else {
		// Detection itself may race migration/reuse; check again at dispatch.
		if err := validate(); err != nil {
			return nil, fmt.Errorf("validate process before capture: %w", err)
		}
		captureCtx, cancelCapture := context.WithTimeout(ctx, options.captureTimeout(language))
		captureStarted := time.Now()
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
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snapshot.DurationMS == 0 {
		snapshot.DurationMS = uint64((time.Since(started) + time.Millisecond - 1) / time.Millisecond)
	}
	if err := memsnap.LimitOutput(snapshot, options.TopK); err != nil {
		durationMS := snapshot.DurationMS
		snapshot = memsnap.Failed("limit runtime capture output: " + err.Error())
		snapshot.DurationMS = durationMS
		if err := memsnap.LimitOutput(snapshot, options.TopK); err != nil {
			return nil, err
		}
	}
	if err := validate(); err != nil {
		return nil, fmt.Errorf("validate process before persistence: %w", err)
	}
	result := &Result{
		Identity: identity, Language: language, CaptureTime: now,
		SamplingSeed: seed, Snapshot: snapshot,
	}
	if options.Save != nil {
		if err := options.Save(ctx, result); err != nil {
			return result, err
		}
	}
	return result, nil
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
	if request.TopK <= 0 || request.TopK > memsnap.MaxTopK {
		return nil, fmt.Errorf("runtime capture top-K must be in [1, %d], got %d",
			memsnap.MaxTopK, request.TopK)
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
	if captureErr := ctx.Err(); captureErr != nil {
		return nil, fmt.Errorf("runtime capture terminated: %w", captureErr)
	}
	if snapshot == nil {
		return nil, errors.New("runtime capture returned no snapshot")
	}
	return snapshot, nil
}
