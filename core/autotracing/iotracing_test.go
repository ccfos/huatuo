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

package autotracing

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/procfs/blockdevice"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestHandleIotracingEventReturnsPendingResult(t *testing.T) {
	const taskID = "iotracing-test-task"

	pending := &pendingIOTracingReason{
		reason:   &reasonSnapshot{Type: string(ioReasonUtil)},
		received: make(chan struct{}),
		result:   make(chan error, 1),
	}
	pendingReasons.Store(taskID, pending)
	t.Cleanup(func() {
		pendingReasons.Delete(taskID)
	})

	err := handleIotracingEvent(
		&toolstream.Session{
			Session: &transport.Session{
				TaskID: taskID,
			},
		},
		&types.IOTracingSnapshot{},
	)
	if err != nil {
		t.Fatalf("handleIotracingEvent() error = %v", err)
	}

	select {
	case <-pending.received:
	default:
		t.Fatal("handleIotracingEvent() did not mark the snapshot as received")
	}
	select {
	case result := <-pending.result:
		if result != nil {
			t.Fatalf("pending result = %v, want nil", result)
		}
	default:
		t.Fatal("handleIotracingEvent() did not return the pending result")
	}
	if _, ok := pendingReasons.Load(taskID); ok {
		t.Fatal("handleIotracingEvent() left the pending reason in the registry")
	}
}

func TestDeleteMissingDiskState(t *testing.T) {
	rawStats := map[string]*blockdevice.Diskstats{
		"present": {},
		"missing": {},
	}
	metrics := map[string]diskStatus{
		"present": {},
		"missing": {},
	}

	deleteMissingDiskState(
		rawStats,
		metrics,
		map[string]struct{}{"present": {}},
	)

	if _, ok := rawStats["missing"]; ok {
		t.Error("raw stats retained a missing device")
	}
	if _, ok := metrics["missing"]; ok {
		t.Error("metrics retained a missing device")
	}
	if _, ok := rawStats["present"]; !ok {
		t.Error("raw stats removed a present device")
	}
	if _, ok := metrics["present"]; !ok {
		t.Error("metrics removed a present device")
	}
}

func TestWaitForSnapshotTimeouts(t *testing.T) {
	t.Run("snapshot not received", func(t *testing.T) {
		const taskID = "missing-snapshot-task"
		pending := &pendingIOTracingReason{
			received: make(chan struct{}),
			result:   make(chan error, 1),
		}
		pendingReasons.Store(taskID, pending)
		t.Cleanup(func() {
			pendingReasons.Delete(taskID)
		})

		err := waitForSnapshot(
			context.Background(),
			taskID,
			pending,
			time.Millisecond,
			time.Second,
		)
		if err == nil || err.Error() != "iotracing exited without sending a snapshot" {
			t.Fatalf("waitForSnapshot() error = %v", err)
		}
		if _, ok := pendingReasons.Load(taskID); ok {
			t.Fatal("waitForSnapshot() left the pending reason in the registry")
		}
	})

	t.Run("snapshot save", func(t *testing.T) {
		pending := &pendingIOTracingReason{
			received: make(chan struct{}),
			result:   make(chan error, 1),
		}
		close(pending.received)

		err := waitForSnapshot(
			context.Background(),
			"saving-snapshot-task",
			pending,
			time.Second,
			time.Millisecond,
		)
		if err == nil ||
			err.Error() != "timed out waiting for iotracing snapshot save" {
			t.Fatalf("waitForSnapshot() error = %v", err)
		}
	})
}

func TestIOTracingCancellationPreservesIndependentFailures(t *testing.T) {
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(contextErr.Error(), func(t *testing.T) {
			for _, name := range []string{"pure cancellation", "output", "execution", "cleanup", "cleanup without output"} {
				t.Run(name, func(t *testing.T) {
					tracer, err := newIOTracer(validIOTracingConfig())
					if err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithCancel(t.Context())
					if errors.Is(contextErr, context.DeadlineExceeded) {
						cancel()
						ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
					}
					defer cancel()

					failure := &executil.RunError{ContextErr: contextErr}
					var cause error
					switch name {
					case "output":
						cause = executil.ErrOutputLimitExceeded
						failure.OutputErr = cause
					case "execution":
						cause = errors.New("tool exited with status 7")
						failure.ExecutionErr = cause
					case "cleanup", "cleanup without output":
						cause = syscall.EPERM
						failure.CleanupErr = fmt.Errorf("signal process group: %w", errors.Join(executil.ErrStopFailed, cause))
					}
					reason := &reasonSnapshot{Type: string(ioReasonUtil)}
					var taskID string
					t.Cleanup(func() { pendingReasons.Delete(taskID) })
					err = tracer.runSnapshot(ctx, reason, func(runCtx context.Context, spec executil.Spec, _ ...executil.Option) (*executil.Result, error) {
						if runCtx != ctx || filepath.Base(spec.Path) != iotracingToolName {
							t.Fatalf("unexpected command context or path: %s", spec.Path)
						}
						for index, arg := range spec.Args {
							if arg == "--task-id" && index+1 < len(spec.Args) {
								taskID = spec.Args[index+1]
							}
						}
						pending, ok := pendingReasons.Load(taskID)
						if !ok || pending.(*pendingIOTracingReason).reason != reason {
							t.Fatal("command started without its pending reason")
						}
						cancel()
						if name == "cleanup without output" {
							return nil, failure
						}
						return &executil.Result{Stderr: []byte("iotracing diagnostic")}, failure
					})
					if taskID == "" {
						t.Fatal("iotracing command was not run")
					}
					if _, ok := pendingReasons.Load(taskID); ok {
						t.Error("failed command retained its pending reason")
					}
					if cause == nil {
						if err != nil {
							t.Fatalf("pure cancellation: %v", err)
						}
						return
					}
					if !errors.Is(err, contextErr) || !errors.Is(err, cause) {
						t.Fatalf("lost cancellation or independent failure: %v", err)
					}
					var got *executil.RunError
					if !errors.As(err, &got) || got != failure {
						t.Fatalf("lost RunError: %v", err)
					}
					if failure.CleanupErr != nil && !errors.Is(err, executil.ErrStopFailed) {
						t.Errorf("lost stop failure: %v", err)
					}
					if name != "cleanup without output" && !strings.Contains(err.Error(), "iotracing diagnostic") {
						t.Errorf("lost diagnostic: %v", err)
					}
				})
			}
		})
	}
}

func TestNewIOTracer(t *testing.T) {
	tests := []struct {
		name          string
		updateConfig  func(*Config)
		expectedError string
	}{
		{
			name: "valid config",
		},
		{
			name: "zero read throughput threshold",
			updateConfig: func(config *Config) {
				config.IOTracing.RbpsThreshold = 0
			},
			expectedError: "io read bps threshold must be positive",
		},
		{
			name: "zero write throughput threshold",
			updateConfig: func(config *Config) {
				config.IOTracing.WbpsThreshold = 0
			},
			expectedError: "io write bps threshold must be positive",
		},
		{
			name: "zero utilization threshold",
			updateConfig: func(config *Config) {
				config.IOTracing.UtilThreshold = 0
			},
			expectedError: "io util threshold must be positive",
		},
		{
			name: "zero await threshold",
			updateConfig: func(config *Config) {
				config.IOTracing.AwaitThreshold = 0
			},
			expectedError: "io await threshold must be positive",
		},
		{
			name: "zero tracing duration",
			updateConfig: func(config *Config) {
				config.IOTracing.RunTracingToolTimeout = 0
			},
			expectedError: "io tracing duration must be positive",
		},
		{
			name: "zero process limit",
			updateConfig: func(config *Config) {
				config.IOTracing.MaxProcDump = 0
			},
			expectedError: "io max process dump must be positive",
		},
		{
			name: "zero file limit",
			updateConfig: func(config *Config) {
				config.IOTracing.MaxFilesPerProcDump = 0
			},
			expectedError: "io max files per process dump must be positive",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validIOTracingConfig()
			if test.updateConfig != nil {
				test.updateConfig(config)
			}

			tracer, err := newIOTracer(config)
			if test.expectedError == "" {
				if err != nil {
					t.Fatalf("newIOTracer() error = %v", err)
				}
				if tracer == nil {
					t.Fatal("newIOTracer() returned nil tracer")
				}
				return
			}
			if err == nil {
				t.Fatalf("newIOTracer() error = nil, want %q", test.expectedError)
			}
			if !strings.Contains(err.Error(), test.expectedError) {
				t.Fatalf("newIOTracer() error = %q, want substring %q", err, test.expectedError)
			}
		})
	}
}

func TestNewIOTracerBindsConfig(t *testing.T) {
	config := validIOTracingConfig()

	tracer, err := newIOTracer(config)
	if err != nil {
		t.Fatalf("newIOTracer() error = %v", err)
	}

	config.IOTracing.RbpsThreshold = 999
	config.IOTracing.RunTracingToolTimeout = 999
	config.IOTracing.MaxProcDump = 999
	config.IOTracing.MaxFilesPerProcDump = 999

	if tracer.thresholds.RBPSThreshold != 1 {
		t.Errorf("read bps threshold = %d, want 1", tracer.thresholds.RBPSThreshold)
	}
	if tracer.runDurationSeconds != 5 {
		t.Errorf("run duration = %d, want 5", tracer.runDurationSeconds)
	}
	if tracer.maxProcesses != 10 {
		t.Errorf("max processes = %d, want 10", tracer.maxProcesses)
	}
	if tracer.maxFilesPerProcess != 5 {
		t.Errorf("max files per process = %d, want 5", tracer.maxFilesPerProcess)
	}
}

func validIOTracingConfig() *Config {
	config := &Config{}
	config.IOTracing.RbpsThreshold = 1
	config.IOTracing.WbpsThreshold = 1
	config.IOTracing.UtilThreshold = 1
	config.IOTracing.AwaitThreshold = 1
	config.IOTracing.RunTracingToolTimeout = 5
	config.IOTracing.MaxProcDump = 10
	config.IOTracing.MaxFilesPerProcDump = 5
	return config
}
