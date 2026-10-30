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

package profile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"

	"github.com/ccfos/huatuo/internal/log"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

// fakeProfiler completes without collecting samples.
type fakeProfiler struct{}

func (fakeProfiler) Start(context.Context) error { return nil }
func (fakeProfiler) ReadDataLoop(_ context.Context, enqueue func(*pipeline.SampleBatch) error) error {
	return enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: 1}}})
}

func (fakeProfiler) Finalize(context.Context, func(*pipeline.SampleBatch) error) error { return nil }

func (fakeProfiler) Stop() error { return nil }

func testAggregationConfig() pipeline.AggregationConfig {
	return pipeline.AggregationConfig{ProfileType: profiling.ProfileTypeCpuSample}
}

func newMeta(implementation profiling.Implementation, typ profiling.Type) *ProfilerMeta {
	return &ProfilerMeta{
		Type:              typ,
		Implementation:    implementation,
		Description:       "fake",
		Impl:              fakeProfiler{},
		AggregationConfig: testAggregationConfig(),
	}
}

func TestRunLogsDataReadingLoopLifecycleOnce(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := RunConfig{
		Duration:          0,
		OutputFormat:      output.FormatJSON,
		OutputDestination: t.TempDir(),
	}
	if err := Run(ctx, cfg, newMeta(profiling.ImplementationNative, profiling.TypeCPU)); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	const started = "data reading loop started"
	const ended = "data reading loop ended"
	output := logs.String()
	if got := strings.Count(output, started); got != 1 {
		t.Fatalf(
			"%q log count = %d, want 1; logs: %s",
			started,
			got,
			output,
		)
	}

	if got := strings.Count(output, ended); got != 1 {
		t.Fatalf(
			"%q log count = %d, want 1; logs: %s",
			ended,
			got,
			output,
		)
	}

	if strings.Index(output, started) > strings.Index(output, ended) {
		t.Fatalf("data loop lifecycle logs out of order: %s", output)
	}
}

type failingProfiler struct {
	fakeProfiler
	startErr, finalErr, stopErr error
}

func (p failingProfiler) Start(context.Context) error { return p.startErr }
func (p failingProfiler) Finalize(context.Context, func(*pipeline.SampleBatch) error) error {
	return p.finalErr
}
func (p failingProfiler) Stop() error { return p.stopErr }

func TestRunPropagatesFailures(t *testing.T) {
	failure := errors.New("test failure")
	for _, phase := range []string{"start", "finalize", "stop"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			provider := failingProfiler{}
			cfg, received := newRunOutput(t)
			switch phase {
			case "start":
				provider.startErr = failure
			case "finalize":
				provider.finalErr = failure
			case "stop":
				provider.stopErr = failure
			}

			err := Run(
				ctx,
				cfg,
				&ProfilerMeta{
					Impl:              provider,
					AggregationConfig: testAggregationConfig(),
				},
			)
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}

			if received.commits.Load() != 0 {
				t.Fatalf("committed output after %s failed", phase)
			}
		})
	}
}

type runOutput struct {
	commits atomic.Int64
	records atomic.Int64
}

// Observe completion through the public output boundary, including empty runs.
func newRunOutput(t *testing.T) (RunConfig, *runOutput) {
	t.Helper()

	// Long subtest names in t.TempDir paths can exceed the Unix socket limit.
	dir, err := os.MkdirTemp("", "huatuo-profile-")
	if err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove output directory: %v", err)
		}
	})

	path := filepath.Join(dir, "output.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	received := &runOutput{}
	toolstream.Register(server, profiling.StoreServiceName, func(
		_ context.Context,
		request *toolstream.Request[types.AggregationWindow],
	) error {
		if request.Commit {
			received.commits.Add(1)
		}
		if request.Data != nil {
			for _, sample := range request.Data.Aggregated.Samples {
				received.records.Add(sample.Value)
			}
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
	return RunConfig{
		OutputFormat:      output.FormatRemoteStore,
		OutputDestination: path,
	}, received
}

func TestRunPropagatesOutputFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	collector := &loopProfiler{read: func(_ context.Context, enqueue func(*pipeline.SampleBatch) error) error {
		// The formatter opens lazily; replace its file destination before delivery.
		if err := os.Mkdir(path, 0o755); err != nil {
			return err
		}
		return enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: 1}}})
	}}
	err := Run(t.Context(), RunConfig{
		OutputFormat:      output.FormatJSON,
		OutputDestination: path,
	}, &ProfilerMeta{
		Impl:              collector,
		AggregationConfig: testAggregationConfig(),
	})
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "open" || pathErr.Path != path {
		t.Fatalf("Run() error = %v, want output open failure", err)
	}
	if !collector.stopped {
		t.Fatal("collector was not stopped after output failure")
	}
}

type loopProfiler struct {
	fakeProfiler
	read      func(context.Context, func(*pipeline.SampleBatch) error) error
	stopErr   error
	final     func(context.Context, func(*pipeline.SampleBatch) error) error
	onStop    func()
	loopEnded bool
	stopped   bool
}

func (p *loopProfiler) ReadDataLoop(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
	defer func() { p.loopEnded = true }()
	return p.read(ctx, enqueue)
}

func (p *loopProfiler) Finalize(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
	if !p.loopEnded {
		return errors.New("finalization started before read loop ended")
	}

	if p.final != nil {
		return p.final(ctx, enqueue)
	}

	return nil
}

func (p *loopProfiler) Stop() error {
	if !p.loopEnded {
		return errors.New("collector stopped before read loop ended")
	}

	if p.onStop != nil {
		p.onStop()
	}

	p.stopped = true

	return p.stopErr
}

func TestRunCompletion(t *testing.T) {
	failure := errors.New("read samples failed")
	stopFailure := errors.New("stop collector failed")
	for _, tc := range []struct {
		name             string
		duration         time.Duration
		cause            error
		finalBatch       bool
		cancelBeforeRun  bool
		cancelDuringStop bool
		loopErr          error
		stopErr          error
		wantErr          error
		wantFinal        bool
	}{
		{
			name:      "natural completion",
			wantFinal: true,
		},
		{
			name:      "normal duration",
			duration:  time.Millisecond,
			wantFinal: true,
		},
		{
			name:       "final batch after normal stop",
			duration:   time.Millisecond,
			finalBatch: true,
			wantFinal:  true,
		},
		{
			name:      "requested stop",
			cause:     ErrStopRequested,
			wantFinal: true,
		},
		{
			name:    "external cancellation during collection",
			cause:   context.Canceled,
			wantErr: context.Canceled,
		},
		{
			name:    "normal stop with collection failure",
			cause:   ErrStopRequested,
			loopErr: failure,
			wantErr: failure,
		},
		{
			name:            "external cancellation with clean loop",
			cancelBeforeRun: true,
			wantErr:         context.Canceled,
		},
		{
			name:            "external cancellation with final batch",
			cancelBeforeRun: true,
			finalBatch:      true,
			wantErr:         context.Canceled,
		},
		{
			name:    "read failure",
			loopErr: failure,
			wantErr: failure,
		},
		{
			name:    "loop cancellation",
			loopErr: context.Canceled,
			wantErr: context.Canceled,
		},
		{
			name:    "joined cancellation and failure",
			loopErr: errors.Join(context.Canceled, failure),
			wantErr: failure,
		},
		{
			name:    "wrapped joined failure",
			loopErr: fmt.Errorf("read loop: %w", errors.Join(context.Canceled, failure)),
			wantErr: failure,
		},
		{
			name:     "failure at duration",
			duration: time.Millisecond,
			loopErr:  errors.Join(context.Canceled, failure),
			wantErr:  failure,
		},
		{
			name:      "stop failure",
			stopErr:   stopFailure,
			wantErr:   stopFailure,
			wantFinal: true,
		},
		{
			name:             "external cancellation during cleanup",
			cancelDuringStop: true,
			wantErr:          context.Canceled,
			wantFinal:        true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			if tc.cancelBeforeRun {
				cancel(context.Canceled)
			}

			cfg, received := newRunOutput(t)
			finalized := false
			collector := &loopProfiler{
				stopErr: tc.stopErr,
				read: func(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
					if tc.cause != nil {
						cancel(tc.cause)
						<-ctx.Done()
					}

					if tc.duration != 0 {
						<-ctx.Done()
					}

					if tc.finalBatch {
						// A read already in progress can deliver its partial batch.
						for range 8 {
							if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: 1}}}); err != nil {
								return err
							}
						}
					}

					return tc.loopErr
				},
				final: func(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
					finalized = true
					if err := ctx.Err(); err != nil {
						return err
					}

					return enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: 7}}})
				},
			}
			if tc.cancelDuringStop {
				collector.onStop = func() { cancel(context.Canceled) }
			}

			cfg.Duration = tc.duration
			mode := pipeline.AggregationModeStreaming
			if tc.finalBatch {
				mode = pipeline.AggregationModeOneShot
			}

			err := Run(ctx, cfg, &ProfilerMeta{
				Impl:              collector,
				AggregationMode:   mode,
				AggregationConfig: testAggregationConfig(),
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Run() error = %v, want %v", err, tc.wantErr)
			}

			if tc.loopErr != nil && errors.Is(tc.loopErr, context.Canceled) && !errors.Is(err, context.Canceled) {
				t.Fatalf("Run() lost cancellation from loop error: %v", err)
			}

			if tc.cause == nil && !tc.cancelBeforeRun && !tc.cancelDuringStop && ctx.Err() != nil {
				t.Fatalf("runner canceled caller context: %v", ctx.Err())
			}

			if finalized != tc.wantFinal || !collector.stopped {
				t.Fatalf(
					"finalized=%t want=%t stopped=%t",
					finalized,
					tc.wantFinal,
					collector.stopped,
				)
			}

			if tc.wantErr == nil {
				wantRecords := int64(7)
				if tc.finalBatch {
					wantRecords += 8
				}

				if got := received.records.Load(); got != wantRecords {
					t.Fatalf("delivered records=%d want=%d", got, wantRecords)
				}
			}

			wantWrites := int64(0)
			if tc.wantErr == nil {
				wantWrites = 1
			}

			if got := received.commits.Load(); got != wantWrites {
				t.Fatalf("completion writes=%d want=%d", got, wantWrites)
			}
		})
	}
}

func TestRunRejectsInvalidFormatterBeforeCollection(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	startErr := errors.New("collector must not start")
	cfg := RunConfig{
		Duration:          0,
		OutputFormat:      output.FormatRemoteStore,
		OutputDestination: "",
	}
	err := Run(ctx, cfg, &ProfilerMeta{
		Impl:              failingProfiler{startErr: startErr},
		AggregationConfig: testAggregationConfig(),
	})
	if err == nil || !strings.Contains(err.Error(), "--output-path must not be empty") {
		t.Fatalf("Run() error = %v, want invalid socket destination", err)
	}

	if errors.Is(err, startErr) {
		t.Fatal("started collection before validating formatter")
	}
}

func TestRunClosesRemoteOutputOnCollectionFailure(t *testing.T) {
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
			var collector Profiler = &loopProfiler{
				read: func(context.Context, func(*pipeline.SampleBatch) error) error { return failure },
			}
			if phase == "start" {
				collector = failingProfiler{startErr: failure}
			}

			if phase == "stop" {
				collector = failingProfiler{stopErr: failure}
			}

			cfg := RunConfig{
				Duration:          0,
				OutputFormat:      output.FormatRemoteStore,
				OutputDestination: path,
			}
			cfg.ID = "task-1"
			err = Run(ctx, cfg, &ProfilerMeta{
				Impl:              collector,
				AggregationConfig: testAggregationConfig(),
			})
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

func TestRunNormalStopPreservesCollectorErrors(t *testing.T) {
	failure := errors.New("incomplete final collection")
	for _, tc := range []struct {
		name      string
		readError error
	}{
		{name: "complete"}, {
			name:      "failed",
			readError: errors.Join(context.Canceled, failure),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, received := newRunOutput(t)
			collector := &loopProfiler{read: func(
				ctx context.Context,
				_ func(*pipeline.SampleBatch) error,
			) error {
				<-ctx.Done()
				if !IsNormalStop(ctx) {
					return context.Cause(ctx)
				}

				return tc.readError
			}}
			cfg.Duration = time.Nanosecond
			err := Run(
				t.Context(),
				cfg,
				&ProfilerMeta{
					Impl:              collector,
					AggregationMode:   pipeline.AggregationModeOneShot,
					AggregationConfig: testAggregationConfig(),
				},
			)
			if tc.readError == nil {
				if err != nil || received.commits.Load() != 1 {
					t.Fatalf("error=%v completions=%d", err, received.commits.Load())
				}
			} else if !errors.Is(err, failure) || !errors.Is(err, context.Canceled) || received.commits.Load() != 0 {
				t.Fatalf("error=%v completions=%d", err, received.commits.Load())
			}
		})
	}
}

func TestRunRemoteTaskIdentity(t *testing.T) {
	for _, taskID := range []string{"scheduled-task", ""} {
		t.Run("task="+taskID, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile.sock")
			server, err := toolstream.NewServer(path)
			if err != nil {
				t.Fatal(err)
			}

			received := make(chan *toolstream.Request[types.AggregationWindow], 1)
			toolstream.Register(server, profiling.StoreServiceName, func(
				_ context.Context,
				request *toolstream.Request[types.AggregationWindow],
			) error {
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
			if err := Run(t.Context(), RunConfig{
				ID:                taskID,
				OutputFormat:      output.FormatRemoteStore,
				OutputDestination: path,
			}, task); err != nil {
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

func TestRunAggregationFailureStopsCollectionWithoutCommit(t *testing.T) {
	cfg, received := newRunOutput(t)
	stopFailure := errors.New("collector cleanup failed")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	watchdog := time.AfterFunc(5*time.Second, cancel)
	defer watchdog.Stop()
	collector := &loopProfiler{
		stopErr: stopFailure,
		read: func(_ context.Context, enqueue func(*pipeline.SampleBatch) error) error {
			if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{
				Labels: []types.ProfileLabel{{Name: "invalid"}},
				Value:  1,
			}}}); err != nil {
				return err
			}

			// A failed consumer must release a producer waiting on a full queue.
			for {
				if err := enqueue(&pipeline.SampleBatch{}); err != nil {
					return err
				}
			}
		},
	}
	err := Run(ctx, cfg, &ProfilerMeta{
		Impl:              collector,
		AggregationConfig: testAggregationConfig(),
	})
	if err == nil || !strings.Contains(err.Error(), "aggregate sample 0") || !errors.Is(err, stopFailure) {
		t.Fatalf("Run() = %v, want aggregation and cleanup failures", err)
	}
	if ctx.Err() != nil || !collector.stopped || received.commits.Load() != 0 {
		t.Fatalf("context=%v stopped=%v commits=%d", ctx.Err(), collector.stopped, received.commits.Load())
	}
}

func TestRunFinalFormatFailurePreventsPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.svg")
	collector := &loopProfiler{
		read: func(_ context.Context, enqueue func(*pipeline.SampleBatch) error) error {
			// Distinct stacks fit int64 individually but overflow the SVG total.
			// Complete must reject the result before publishing any file.
			return enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{
				{UserFrames: []types.ProfileFrame{{Name: "accepted"}}, Value: 1},
				{UserFrames: []types.ProfileFrame{{Name: "rejected"}}, Value: math.MaxInt64},
			}})
		},
	}
	err := Run(t.Context(), RunConfig{
		OutputFormat:      output.FormatSVG,
		OutputDestination: path,
	}, &ProfilerMeta{
		Impl:              collector,
		AggregationMode:   pipeline.AggregationModeOneShot,
		AggregationConfig: testAggregationConfig(),
	})
	if err == nil || !strings.Contains(err.Error(), "total weight overflows") {
		t.Fatalf("Run() = %v, want final window failure", err)
	}
	if !collector.stopped {
		t.Fatal("collector was not stopped")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed task published output: %v", err)
	}
}

func TestRunRequestedStopPublishesFinalSamples(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cfg, received := newRunOutput(t)
	collector := &loopProfiler{
		read: func(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
			cancel(ErrStopRequested)
			<-ctx.Done()
			if !IsNormalStop(ctx) {
				return context.Cause(ctx)
			}

			return nil
		},
		final: func(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Final delivery can exceed the queue capacity after collection stops.
			for range 128 {
				if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: 4096}}}); err != nil {
					return err
				}
			}

			for range 128 {
				if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: -4096}}}); err != nil {
					return err
				}
			}

			return enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: 7}}})
		},
	}
	err := Run(ctx, cfg, &ProfilerMeta{Impl: collector, AggregationMode: pipeline.AggregationModeOneShot, AggregationConfig: testAggregationConfig()})
	if err != nil || received.commits.Load() != 1 || received.records.Load() != 7 {
		t.Fatalf("error=%v commits=%d samples=%d", err, received.commits.Load(), received.records.Load())
	}
}

func TestRunFailureDuringNormalFinalizationForbidsCommit(t *testing.T) {
	cfg, received := newRunOutput(t)
	cfg.Duration = time.Millisecond
	collector := &loopProfiler{
		read: func(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
			<-ctx.Done()
			if !IsNormalStop(ctx) {
				return context.Cause(ctx)
			}

			return nil
		},
		final: func(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
			// Streaming aggregation rejects overflow while the collector is finalizing.
			if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: math.MaxInt64}, {Value: 1}}}); err != nil {
				return err
			}

			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-deadline.C:
					return errors.New("pipeline did not reject final delivery after failure")
				case <-ticker.C:
					if err := enqueue(&pipeline.SampleBatch{}); err != nil {
						return err
					}
				}
			}
		},
	}
	err := Run(t.Context(), cfg, &ProfilerMeta{Impl: collector, AggregationConfig: testAggregationConfig()})
	if err == nil || !strings.Contains(err.Error(), "overflow") || received.commits.Load() != 0 {
		t.Fatalf("error=%v commits=%d", err, received.commits.Load())
	}
}

func TestNormalStopDoesNotAcceptJoinedFailure(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.Join(ErrStopRequested, errors.New("collection failed")))
	if IsNormalStop(ctx) {
		t.Fatal("joined failure accepted as normal stop")
	}
}

func TestRunRejectsCallerDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
	}{
		{
			name:    "expired deadline",
			timeout: -time.Second,
		},
		{
			name:    "future deadline",
			timeout: time.Hour,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, cancel := context.WithTimeout(t.Context(), tc.timeout)
			defer cancel()
			ctx, cancelChild := context.WithCancel(parent)
			defer cancelChild()
			destination := filepath.Join(t.TempDir(), "output")
			collector := &initializingProfiler{start: func(context.Context) error {
				t.Fatal("collector started with a caller deadline")
				return nil
			}}
			err := Run(ctx, RunConfig{
				Duration:          time.Second,
				OutputFormat:      output.FormatJSON,
				OutputDestination: destination,
			}, &ProfilerMeta{
				Impl:              collector,
				AggregationConfig: testAggregationConfig(),
			})
			if err == nil || err.Error() != "profiling context must not have a deadline; use RunConfig.Duration" {
				t.Fatalf("Run() error = %v, want caller deadline rejection", err)
			}

			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("output initialized before deadline rejection: %v", err)
			}
		})
	}
}

type initializingProfiler struct {
	loopProfiler
	start func(context.Context) error
}

func (p *initializingProfiler) Start(ctx context.Context) error { return p.start(ctx) }

func TestRunDurationStartsAfterInitialization(t *testing.T) {
	cfg, received := newRunOutput(t)
	cfg.Duration = 10 * time.Millisecond
	var initializedDone <-chan struct{}
	collector := &initializingProfiler{start: func(ctx context.Context) error {
		initializedDone = ctx.Done()
		// Initialization must not consume the collection duration.
		time.Sleep(2 * cfg.Duration)
		return ctx.Err()
	}}
	collector.read = func(ctx context.Context, _ func(*pipeline.SampleBatch) error) error {
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), ErrCollectionDurationElapsed) {
			return context.Cause(ctx)
		}

		select {
		case <-initializedDone:
			t.Fatal("normal collection stop canceled sampling resources")
		default:
		}

		return nil
	}
	collector.final = func(ctx context.Context, _ func(*pipeline.SampleBatch) error) error {
		if ctx.Done() != initializedDone {
			t.Fatal("Start and Finalize received different task contexts")
		}

		return ctx.Err()
	}
	err := Run(t.Context(), cfg, &ProfilerMeta{Impl: collector, AggregationConfig: testAggregationConfig()})
	if err != nil || received.commits.Load() != 1 {
		t.Fatalf("error=%v commits=%d", err, received.commits.Load())
	}
}

func TestRunNaturalCompletionStopsDurationTimerBeforeCleanup(t *testing.T) {
	cfg, received := newRunOutput(t)
	cfg.Duration = 20 * time.Millisecond
	var collectionDone <-chan struct{}
	collector := &loopProfiler{read: func(ctx context.Context, _ func(*pipeline.SampleBatch) error) error {
		collectionDone = ctx.Done()
		return nil
	}, onStop: func() {
		// Cleanup may outlive the configured sampling duration.
		time.Sleep(2 * cfg.Duration)
		select {
		case <-collectionDone:
			t.Error("collection timer remained active during cleanup")
		default:
		}
	}}
	err := Run(t.Context(), cfg, &ProfilerMeta{Impl: collector, AggregationConfig: testAggregationConfig()})
	if err != nil || received.commits.Load() != 1 {
		t.Fatalf("error=%v commits=%d", err, received.commits.Load())
	}
}

func TestRunRemotePhysicalUsagePublishesRetainedTotals(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "live.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}

	values := make(chan int64, 16)
	var commits atomic.Int64
	toolstream.Register(server, profiling.StoreServiceName, func(
		ctx context.Context,
		request *toolstream.Request[types.AggregationWindow],
	) error {
		if request.Commit {
			commits.Add(1)
		}

		if request.Data == nil {
			return nil
		}

		w := request.Data
		if w.ProfileType != profiling.ProfileTypePhysicalUsage || w.Aggregated.DurationNanos != 0 || w.Aggregated.TimeNanos <= 0 {
			return errors.New("remote physical usage metadata is not a snapshot")
		}

		var total int64
		for _, sample := range w.Aggregated.Samples {
			total += sample.Value
		}

		select {
		case values <- total:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})

	waitForValue := func(want int64) error {
		for {
			select {
			case got := <-values:
				if got == want {
					return nil
				}
			case <-ctx.Done():
				return fmt.Errorf("wait for remote live value %d: %w", want, ctx.Err())
			}
		}
	}
	collector := &loopProfiler{read: func(_ context.Context, enqueue func(*pipeline.SampleBatch) error) error {
		if err := waitForValue(0); err != nil {
			return err
		}

		if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: 8192}}}); err != nil {
			return err
		}

		// Two successive windows with no new events must both carry the live total.
		for range 2 {
			if err := waitForValue(8192); err != nil {
				return err
			}
		}

		if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: -8192}}}); err != nil {
			return err
		}

		return waitForValue(0)
	}}
	err = Run(t.Context(), RunConfig{
		OutputFormat:        output.FormatRemoteStore,
		OutputDestination:   path,
		AggregationInterval: time.Millisecond,
	}, &ProfilerMeta{
		Impl:              collector,
		AggregationMode:   pipeline.AggregationModeStreaming,
		AggregationConfig: pipeline.AggregationConfig{ProfileType: profiling.ProfileTypePhysicalUsage},
	})
	if err != nil {
		t.Fatal(err)
	}

	if commits.Load() != 1 || !collector.stopped {
		t.Fatalf("commits=%d stopped=%t", commits.Load(), collector.stopped)
	}
}

func TestRunStopDuringFinalDelivery(t *testing.T) {
	for _, cause := range []error{ErrStopRequested, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cfg, received := newRunOutput(t)
			delivered := 0
			collector := &loopProfiler{
				read: func(context.Context, func(*pipeline.SampleBatch) error) error { return nil },
				final: func(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
					for range 128 {
						// Wait for the framework to observe abnormal cancellation deterministically.
						if delivered == 1 && errors.Is(cause, context.Canceled) {
							<-ctx.Done()
						}

						if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{Value: 1}}}); err != nil {
							return err
						}

						delivered++
						cancel(cause)
					}

					return nil
				},
			}
			err := Run(ctx, cfg, &ProfilerMeta{
				Impl:              collector,
				AggregationMode:   pipeline.AggregationModeOneShot,
				AggregationConfig: testAggregationConfig(),
			})
			if errors.Is(cause, ErrStopRequested) {
				if err != nil || delivered != 128 || received.records.Load() != 128 || received.commits.Load() != 1 {
					t.Fatalf(
						"error=%v delivered=%d records=%d commits=%d",
						err,
						delivered,
						received.records.Load(),
						received.commits.Load(),
					)
				}
			} else if !errors.Is(err, context.Canceled) || delivered != 1 || received.commits.Load() != 0 {
				t.Fatalf(
					"error=%v delivered=%d commits=%d",
					err,
					delivered,
					received.commits.Load(),
				)
			}
		})
	}
}

func TestRunFinalizationTimeout(t *testing.T) {
	for _, phase := range []string{"read loop", "finalize", "publication"} {
		t.Run(phase, func(t *testing.T) {
			cfg, received := newRunOutput(t)
			if phase == "read loop" {
				cfg.Duration = time.Millisecond
			}

			finalized := false
			collector := &loopProfiler{
				read: func(ctx context.Context, _ func(*pipeline.SampleBatch) error) error {
					if phase == "read loop" {
						<-ctx.Done()
						if !IsNormalStop(ctx) {
							return context.Cause(ctx)
						}

						time.Sleep(20 * time.Millisecond)
					}

					return nil
				},
				final: func(ctx context.Context, _ func(*pipeline.SampleBatch) error) error {
					finalized = true
					if phase == "finalize" {
						<-ctx.Done()
						return context.Cause(ctx)
					}

					return nil
				},
			}
			if phase == "publication" {
				collector.onStop = func() { time.Sleep(20 * time.Millisecond) }
			}

			err := run(
				t.Context(),
				cfg,
				&ProfilerMeta{
					Impl:              collector,
					AggregationConfig: testAggregationConfig(),
				},
				5*time.Millisecond,
			)
			if !errors.Is(err, context.DeadlineExceeded) || !collector.stopped || received.commits.Load() != 0 {
				t.Fatalf(
					"error=%v stopped=%t commits=%d",
					err,
					collector.stopped,
					received.commits.Load(),
				)
			}

			if phase == "read loop" && finalized {
				t.Fatal("finalization started after budget expired")
			}
		})
	}
}

func TestRunFinalizationTimeoutUnblocksEnqueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}

	received := make(chan struct{}, 1)
	release := make(chan struct{})
	toolstream.Register(server, profiling.StoreServiceName, func(_ context.Context, _ *toolstream.Request[types.AggregationWindow]) error {
		select {
		case received <- struct{}{}:
		default:
		}

		<-release
		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}

	defer server.Close()
	defer close(release)
	delivered := 0
	largeFrames := []types.ProfileFrame{{Name: strings.Repeat("f", 1<<20)}}
	collector := &loopProfiler{
		read: func(context.Context, func(*pipeline.SampleBatch) error) error { return nil },
		final: func(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
			if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{
				Value:      1,
				UserFrames: largeFrames,
			}}}); err != nil {
				return err
			}

			select {
			case <-received:
			case <-ctx.Done():
				return context.Cause(ctx)
			}

			for range 128 {
				if err := enqueue(&pipeline.SampleBatch{Samples: []aggregation.Sample{{
					Value:      1,
					UserFrames: largeFrames,
				}}}); err != nil {
					return err
				}

				delivered++
			}

			return errors.New("blocked consumer did not apply queue backpressure")
		},
	}
	err = run(
		t.Context(),
		RunConfig{
			OutputFormat:      output.FormatRemoteStore,
			OutputDestination: path,

			AggregationInterval: time.Millisecond,
		},
		&ProfilerMeta{
			Impl:              collector,
			AggregationConfig: testAggregationConfig(),
		},
		500*time.Millisecond,
	)
	if !errors.Is(err, context.DeadlineExceeded) || delivered == 0 || delivered >= 128 || !collector.stopped {
		t.Fatalf(
			"error=%v delivered=%d stopped=%t",
			err,
			delivered,
			collector.stopped,
		)
	}
}

func TestRunTaskContextInterruptsReading(t *testing.T) {
	for _, tc := range []struct {
		name         string
		cancelParent bool
		want         error
	}{
		{
			name:         "caller cancellation",
			cancelParent: true,
			want:         context.Canceled,
		},
		{
			name: "finalization timeout",
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cfg, received := newRunOutput(t)
			if !tc.cancelParent {
				cfg.Duration = time.Millisecond
			}

			var taskDone <-chan struct{}
			finalized := false
			collector := &initializingProfiler{start: func(ctx context.Context) error {
				taskDone = ctx.Done()
				return nil
			}}
			collector.read = func(ctx context.Context, _ func(*pipeline.SampleBatch) error) error {
				if tc.cancelParent {
					cancel()
				}

				<-ctx.Done()
				// A blocking reader must observe task failure even when the collection
				// context already carries an immutable normal-stop cause.
				select {
				case <-taskDone:
					return nil
				case <-time.After(time.Second):
					return errors.New("task cancellation did not interrupt reading")
				}
			}
			collector.final = func(context.Context, func(*pipeline.SampleBatch) error) error {
				finalized = true
				return nil
			}
			err := run(
				ctx,
				cfg,
				&ProfilerMeta{
					Impl:              collector,
					AggregationConfig: testAggregationConfig(),
				},
				5*time.Millisecond,
			)
			if !errors.Is(err, tc.want) || finalized || !collector.stopped || received.commits.Load() != 0 {
				t.Fatalf(
					"error=%v finalized=%t stopped=%t commits=%d",
					err,
					finalized,
					collector.stopped,
					received.commits.Load(),
				)
			}
		})
	}
}

func TestRunFinalLossPreventsPhysicalUsagePublication(t *testing.T) {
	cfg, received := newRunOutput(t)
	collector := &loopProfiler{
		read: func(context.Context, func(*pipeline.SampleBatch) error) error { return nil },
		final: func(_ context.Context, enqueue func(*pipeline.SampleBatch) error) error {
			return enqueue(&pipeline.SampleBatch{DroppedSamples: map[string]uint64{types.DropReasonCollection: 3}})
		},
	}
	err := Run(t.Context(), cfg, &ProfilerMeta{
		Impl:              collector,
		AggregationMode:   pipeline.AggregationModeOneShot,
		AggregationConfig: pipeline.AggregationConfig{ProfileType: profiling.ProfileTypePhysicalUsage},
	})
	if err == nil || !strings.Contains(err.Error(), "physical usage collection lost samples") || received.commits.Load() != 0 || !collector.stopped {
		t.Fatalf(
			"error=%v commits=%d stopped=%t",
			err,
			received.commits.Load(),
			collector.stopped,
		)
	}
}
