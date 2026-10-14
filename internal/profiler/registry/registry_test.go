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

	_ "github.com/ccfos/huatuo/internal/profiler/output/flamegraph"
	_ "github.com/ccfos/huatuo/internal/profiler/output/raw"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler/aggregator"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

// fakeProfiler completes without collecting samples.
type fakeProfiler struct{}

func (fakeProfiler) Start(context.Context) error                   { return nil }
func (fakeProfiler) ReadDataLoop(context.Context, func(any)) error { return nil }
func (fakeProfiler) Stop(context.Context) error                    { return nil }

// fakeAggregator satisfies aggregator.Aggregator with no behavior.
type fakeAggregator struct{}

func (fakeAggregator) Aggregate(any) {}
func (fakeAggregator) Snapshot(context.Context) (*types.ProfilingWindow, error) {
	return nil, nil
}
func (fakeAggregator) Reset() {}

func newMeta(implementation profiling.Implementation, typ profiling.Type) *ProfilerMeta {
	return &ProfilerMeta{
		Type:           typ,
		Implementation: implementation,
		Description:    "fake",
		Impl:           fakeProfiler{},
		Aggregator:     fakeAggregator{},
	}
}

func TestProfileLogsDataReadingLoopLifecycleOnce(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := RunConfig{Duration: 0, OutputFormat: output.FormatJSON, OutputDestination: t.TempDir()}
	if err := Profile(ctx, cfg, newMeta(profiling.ImplementationNative, profiling.TypeCPU)); err != nil {
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

func (p failingProfiler) Start(context.Context) error { return p.startErr }
func (p failingProfiler) Stop(context.Context) error  { return p.stopErr }

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
			cfg := RunConfig{Duration: 0, OutputFormat: output.FormatCollapsed, OutputDestination: t.TempDir()}
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
			err := Profile(ctx, cfg, &ProfilerMeta{Impl: provider, Aggregator: aggr})
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
	onStop    func()
	loopEnded bool
	stopped   bool
}

func (p *loopProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	defer func() { p.loopEnded = true }()
	return p.read(ctx, enqueue)
}

func (p *loopProfiler) Stop(context.Context) error {
	if !p.loopEnded {
		return errors.New("collector stopped before read loop ended")
	}
	if p.onStop != nil {
		p.onStop()
	}
	p.stopped = true
	return p.stopErr
}

func TestProfileCompletionRequiresSuccessfulCollection(t *testing.T) {
	failure := errors.New("read samples failed")
	stopFailure := errors.New("stop collector failed")
	for _, tc := range []struct {
		name             string
		duration         time.Duration
		finalBatch       bool
		cancelBeforeRun  bool
		cancelDuringStop bool
		loopErr          error
		stopErr          error
		wantErr          error
	}{
		{name: "natural completion"},
		{name: "normal duration", duration: time.Millisecond},
		{name: "final batch after normal stop", duration: time.Millisecond, finalBatch: true},
		{name: "external cancellation with clean loop", cancelBeforeRun: true, wantErr: context.Canceled},
		{name: "external cancellation with final batch", cancelBeforeRun: true, finalBatch: true, wantErr: context.Canceled},
		{name: "read failure", loopErr: failure, wantErr: failure},
		{name: "loop cancellation", loopErr: context.Canceled, wantErr: context.Canceled},
		{name: "joined cancellation and failure", loopErr: errors.Join(context.Canceled, failure), wantErr: failure},
		{name: "wrapped joined failure", loopErr: fmt.Errorf("read loop: %w", errors.Join(context.Canceled, failure)), wantErr: failure},
		{name: "failure at duration", duration: time.Millisecond, loopErr: errors.Join(context.Canceled, failure), wantErr: failure},
		{name: "stop failure", stopErr: stopFailure, wantErr: stopFailure},
		{name: "external cancellation during cleanup", cancelDuringStop: true, wantErr: context.Canceled},
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
			if tc.cancelDuringStop {
				collector.onStop = cancel
			}
			cfg := RunConfig{Duration: tc.duration, OutputFormat: format, OutputDestination: "-"}
			mode := aggregator.AggregationModeStreaming
			if tc.finalBatch {
				mode = aggregator.AggregationModeOneShot
			}
			aggr := &completionAggregator{}
			err := Profile(ctx, cfg, &ProfilerMeta{
				Impl: collector, AggregationMode: mode,
				Aggregator: aggr,
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Profile() error = %v, want %v", err, tc.wantErr)
			}
			if tc.loopErr != nil && errors.Is(tc.loopErr, context.Canceled) && !errors.Is(err, context.Canceled) {
				t.Fatalf("Profile() lost cancellation from loop error: %v", err)
			}
			if !tc.cancelBeforeRun && !tc.cancelDuringStop && ctx.Err() != nil {
				t.Fatalf("runner canceled caller context: %v", ctx.Err())
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
	cfg := RunConfig{Duration: 0, OutputFormat: output.FormatRemoteStore, OutputDestination: ""}
	err := Profile(ctx, cfg, &ProfilerMeta{
		Impl:       failingProfiler{startErr: startErr},
		Aggregator: fakeAggregator{},
	})
	if err == nil || !strings.Contains(err.Error(), "--output-path must not be empty") {
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
			cfg := RunConfig{Duration: 0, OutputFormat: output.FormatRemoteStore, OutputDestination: path}
			cfg.ID = "task-1"
			err = Profile(ctx, cfg, &ProfilerMeta{Impl: collector, Aggregator: fakeAggregator{}})
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

type selfTimedProfiler struct{ loopProfiler }

func (*selfTimedProfiler) ManagesDuration() {}

func TestProfileSelfTimedCollectorPreservesErrors(t *testing.T) {
	failure := errors.New("incomplete self-timed collection")
	for _, tc := range []struct {
		name      string
		readError error
	}{
		{name: "complete"}, {name: "failed", readError: errors.Join(context.Canceled, failure)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			renderer := &completionRenderer{}
			const format output.OutputFormat = "self-timed-test"
			output.RegisterRenderer(format, func() output.Renderer { return renderer })
			collector := &selfTimedProfiler{loopProfiler: loopProfiler{read: func(ctx context.Context, _ func(any)) error {
				// A self-timed collector must outlive the runner's configured duration.
				timer := time.NewTimer(10 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return errors.New("runner stopped self-timed collector")
				case <-timer.C:
				}
				return tc.readError
			}}}
			err := Profile(t.Context(), RunConfig{Duration: time.Nanosecond, OutputFormat: format, OutputDestination: "-"}, &ProfilerMeta{Impl: collector, AggregationMode: aggregator.AggregationModeOneShot, Aggregator: fakeAggregator{}})
			if tc.readError == nil {
				if err != nil || renderer.writes != 1 {
					t.Fatalf("error=%v completions=%d", err, renderer.writes)
				}
			} else if !errors.Is(err, failure) || !errors.Is(err, context.Canceled) || renderer.writes != 0 {
				t.Fatalf("error=%v completions=%d", err, renderer.writes)
			}
		})
	}
}

func TestProfileRemoteTaskIdentity(t *testing.T) {
	for _, taskID := range []string{"scheduled-task", ""} {
		t.Run("task="+taskID, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile.sock")
			server, err := toolstream.NewServer(path)
			if err != nil {
				t.Fatal(err)
			}
			received := make(chan *toolstream.Request[types.ProfilingWindow], 1)
			toolstream.Register(server, profiling.StoreServiceName, func(_ context.Context, request *toolstream.Request[types.ProfilingWindow]) error {
				if request.Commit {
					received <- request
				}
				return nil
			})
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := server.Close(); err != nil {
					t.Error(err)
				}
			})
			task := newMeta(profiling.ImplementationNative, profiling.TypeCPU)
			task.ToolName = "profiler"
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := Profile(ctx, RunConfig{ID: taskID, OutputFormat: output.FormatRemoteStore, OutputDestination: path}, task); err != nil {
				t.Fatal(err)
			}
			select {
			case request := <-received:
				if request.TaskID == "" || (taskID != "" && request.TaskID != taskID) || request.ToolName != "profiler" {
					t.Fatalf("commit identity: %+v", request)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
