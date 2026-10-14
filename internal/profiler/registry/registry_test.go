// Copyright 2025, 2026 The HuaTuo Authors
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

package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler/aggregator"
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

// fakeProfiler satisfies Profiler with no behavior. Registry tests exercise
// the lookup tables, not sampling, so a stub is enough.
type fakeProfiler struct{}

func (fakeProfiler) Start(*pcontext.ProfilerContext) error         { return nil }
func (fakeProfiler) ReadDataLoop(context.Context, func(any)) error { return nil }
func (fakeProfiler) Stop(*pcontext.ProfilerContext) error          { return nil }

// fakeAggregator satisfies aggregator.Aggregator with no behavior.
type fakeAggregator struct{}

func (fakeAggregator) Aggregate(any) {}
func (fakeAggregator) Snapshot(context.Context) (*types.ProfilingWindow, error) {
	return nil, nil
}
func (fakeAggregator) Reset() {}

func resetRegistry(t *testing.T) {
	t.Helper()

	saved := profilerRegistry
	profilerRegistry = map[profiling.Implementation]map[profiling.Type]ProfilerMeta{}

	t.Cleanup(func() { profilerRegistry = saved })
}

func newMeta(implementation profiling.Implementation, typ profiling.Type) ProfilerMeta {
	return ProfilerMeta{
		Type:           typ,
		Implementation: implementation,
		Description:    "fake",
		Impl:           fakeProfiler{},
		NewAggregator:  func(*pcontext.ProfilerContext) (aggregator.Aggregator, error) { return fakeAggregator{}, nil },
	}
}

func TestRegisterThenGet(t *testing.T) {
	resetRegistry(t)
	Register(newMeta(profiling.ImplementationNative, profiling.TypeCPU))

	got, err := Get(profiling.ImplementationNative, profiling.TypeCPU)
	if err != nil {
		t.Fatalf(`Get("native", "cpu") error = %v, want nil`, err)
	}

	if got.Implementation != profiling.ImplementationNative || got.Type != profiling.TypeCPU {
		t.Errorf(`Get("native", "cpu") = (%q, %q), want ("native", "cpu")`,
			got.Implementation, got.Type)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	resetRegistry(t)
	Register(newMeta(profiling.ImplementationNative, profiling.TypeCPU))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal(`Register duplicate ("native", "cpu") did not panic`)
		}

		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "duplicate") {
			t.Errorf(`Register panic = %v, want message containing "duplicate"`, r)
		}
	}()

	Register(newMeta(profiling.ImplementationNative, profiling.TypeCPU))
}

func TestGetFallbackMissesWhenTypeAbsent(t *testing.T) {
	resetRegistry(t)
	Register(newMeta(profiling.ImplementationNative, profiling.TypeCPU))

	// The native implementation has no memory provider registered in this test.
	if _, err := Get(profiling.ImplementationNative, profiling.TypeMemory); err == nil {
		t.Fatal(`Get("native", "memory") error = nil, want non-nil`)
	}
}

func TestGetUnknownImplementation(t *testing.T) {
	resetRegistry(t)

	if _, err := Get("unknown", profiling.TypeCPU); err == nil {
		t.Fatal(`Get("unknown", "cpu") error = nil, want non-nil`)
	}
}

func TestProfileLogsDataReadingLoopLifecycleOnce(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })

	ctx, cancel := context.WithCancel(t.Context())
	pctx := &pcontext.ProfilerContext{
		Ctx:          ctx,
		Cancel:       cancel,
		OutputFormat: output.FormatJSON,
		OutputPath:   t.TempDir(),
	}
	if err := Profile(pctx, newMeta(profiling.ImplementationNative, profiling.TypeCPU)); err != nil {
		t.Fatalf("Profile() error = %v, want nil", err)
	}

	const started = "data reading loop started"
	const ended = "data reading loop ended"
	output := logs.String()
	if got := strings.Count(output, started); got != 1 {
		t.Fatalf("%q log count = %d, want 1; logs: %s", started, got, output)
	}
	if got := strings.Count(output, ended); got != 1 {
		t.Fatalf("%q log count = %d, want 1; logs: %s", ended, got, output)
	}
	if strings.Index(output, started) > strings.Index(output, ended) {
		t.Fatalf("data loop lifecycle logs out of order: %s", output)
	}
}

type failingProfiler struct {
	fakeProfiler
	startErr, stopErr error
}

func (p failingProfiler) Start(*pcontext.ProfilerContext) error { return p.startErr }
func (p failingProfiler) Stop(*pcontext.ProfilerContext) error  { return p.stopErr }

type failingAggregator struct {
	fakeAggregator
	err       error
	snapshots *int
}

func (a failingAggregator) Snapshot(context.Context) (*types.ProfilingWindow, error) {
	*a.snapshots++
	return nil, a.err
}

func TestProfilePropagatesFailures(t *testing.T) {
	failure := errors.New("test failure")
	for _, phase := range []string{"start", "stop", "export"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			pctx := &pcontext.ProfilerContext{Ctx: ctx, Cancel: cancel, AggregationMode: aggregator.AggregationModeOneShot, OutputPath: t.TempDir()}
			provider := failingProfiler{}
			snapshots := 0
			aggr := failingAggregator{snapshots: &snapshots}
			switch phase {
			case "start":
				provider.startErr = failure
			case "stop":
				provider.stopErr = failure
			case "export":
				aggr.err = failure
			}
			err := Profile(pctx, ProfilerMeta{Impl: provider, NewAggregator: func(*pcontext.ProfilerContext) (aggregator.Aggregator, error) { return aggr, nil }})
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			if phase != "export" && snapshots != 0 {
				t.Fatalf("exported %d snapshots after %s failed", snapshots, phase)
			}
		})
	}
}

// Completion reaches Renderer.Write even when no aggregation window was emitted.
type completionRenderer struct {
	writes int
}

func (*completionRenderer) Add(*output.Sample) error { return nil }
func (*completionRenderer) IsEmpty() bool            { return true }
func (r *completionRenderer) Write(io.Writer) error {
	r.writes++
	return nil
}

type completionAggregator struct {
	fakeAggregator
	records int
}

func (a *completionAggregator) Aggregate(any) { a.records++ }

type loopProfiler struct {
	fakeProfiler
	read      func(context.Context, func(any)) error
	stopErr   error
	loopEnded bool
	stopped   bool
}

func (p *loopProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	defer func() { p.loopEnded = true }()
	return p.read(ctx, enqueue)
}

func (p *loopProfiler) Stop(*pcontext.ProfilerContext) error {
	if !p.loopEnded {
		return errors.New("collector stopped before read loop ended")
	}
	p.stopped = true
	return p.stopErr
}

func TestProfileCompletionRequiresSuccessfulCollection(t *testing.T) {
	failure := errors.New("read samples failed")
	stopFailure := errors.New("stop collector failed")
	for _, tc := range []struct {
		name            string
		duration        int
		finalBatch      bool
		cancelBeforeRun bool
		loopErr         error
		stopErr         error
		wantErr         error
	}{
		{name: "natural completion"},
		{name: "normal duration", duration: 1},
		{name: "final batch after normal stop", duration: 1, finalBatch: true},
		{name: "external cancellation with clean loop", cancelBeforeRun: true, wantErr: context.Canceled},
		{name: "external cancellation with final batch", cancelBeforeRun: true, finalBatch: true, wantErr: context.Canceled},
		{name: "read failure", loopErr: failure, wantErr: failure},
		{name: "loop cancellation", loopErr: context.Canceled, wantErr: context.Canceled},
		{name: "joined cancellation and failure", loopErr: errors.Join(context.Canceled, failure), wantErr: failure},
		{name: "wrapped joined failure", loopErr: fmt.Errorf("read loop: %w", errors.Join(context.Canceled, failure)), wantErr: failure},
		{name: "failure at duration", duration: 1, loopErr: errors.Join(context.Canceled, failure), wantErr: failure},
		{name: "stop failure", stopErr: stopFailure, wantErr: stopFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelBeforeRun {
				cancel()
			}
			renderer := &completionRenderer{}
			const format output.OutputFormat = "registry-completion-test"
			output.RegisterRenderer(format, func() output.Renderer { return renderer })
			collector := &loopProfiler{stopErr: tc.stopErr, read: func(ctx context.Context, enqueue func(any)) error {
				if tc.duration != 0 {
					<-ctx.Done()
				}
				if tc.finalBatch {
					// Final batches must remain deliverable after collection ends.
					for range 8 {
						enqueue(1)
					}
				}
				return tc.loopErr
			}}
			pctx := &pcontext.ProfilerContext{
				Ctx: ctx, Cancel: cancel, Duration: tc.duration,
				OutputFormat: format, OutputPath: "-",
			}
			if tc.finalBatch {
				pctx.AggregationMode = aggregator.AggregationModeOneShot
			}
			aggr := &completionAggregator{}
			err := Profile(pctx, ProfilerMeta{
				Impl: collector,
				NewAggregator: func(*pcontext.ProfilerContext) (aggregator.Aggregator, error) {
					return aggr, nil
				},
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Profile() error = %v, want %v", err, tc.wantErr)
			}
			if tc.loopErr != nil && errors.Is(tc.loopErr, context.Canceled) && !errors.Is(err, context.Canceled) {
				t.Fatalf("Profile() lost cancellation from loop error: %v", err)
			}
			if !collector.stopped {
				t.Fatal("collector was not stopped after its read loop ended")
			}
			if tc.finalBatch && aggr.records != 8 {
				t.Fatalf("delivered records = %d, want 8", aggr.records)
			}
			wantWrites := 0
			if tc.wantErr == nil {
				wantWrites = 1
			}
			if renderer.writes != wantWrites {
				t.Fatalf("completion writes = %d, want %d", renderer.writes, wantWrites)
			}
		})
	}
}

func TestProfileRejectsInvalidFormatterBeforeCollection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startErr := errors.New("collector must not start")
	pctx := &pcontext.ProfilerContext{Ctx: ctx, Cancel: cancel, OutputFormat: output.FormatRemoteStore}
	err := Profile(pctx, ProfilerMeta{
		Impl:          failingProfiler{startErr: startErr},
		NewAggregator: func(*pcontext.ProfilerContext) (aggregator.Aggregator, error) { return fakeAggregator{}, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "inspect remote profiling destination") {
		t.Fatalf("Profile() error = %v, want invalid socket destination", err)
	}
	if errors.Is(err, startErr) {
		t.Fatal("started collection before validating formatter")
	}
}

func TestProfileClosesRemoteOutputOnCollectionFailure(t *testing.T) {
	for _, phase := range []string{"start", "read", "stop"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "output.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("collection failed")
			var collector Profiler = &loopProfiler{read: func(context.Context, func(any)) error { return failure }}
			if phase == "start" {
				collector = failingProfiler{startErr: failure}
			}
			if phase == "stop" {
				collector = failingProfiler{stopErr: failure}
			}
			pctx := &pcontext.ProfilerContext{
				Ctx: ctx, Cancel: cancel, OutputFormat: output.FormatRemoteStore, OutputPath: path,
				ToolName: "profiler", Version: "1", TracerID: "task-1",
			}
			err = Profile(pctx, ProfilerMeta{Impl: collector, NewAggregator: func(*pcontext.ProfilerContext) (aggregator.Aggregator, error) { return fakeAggregator{}, nil }})
			if !errors.Is(err, failure) {
				t.Fatalf("Profile error = %v, want %v", err, failure)
			}
			conn, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, conn); err != nil {
				t.Fatalf("connection left open after %s failure: %v", phase, err)
			}
		})
	}
}
