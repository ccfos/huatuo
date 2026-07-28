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
	"os"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/procfs/blockdevice"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/toolstream/transport"
	"huatuo-bamai/pkg/types"
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

func TestKillIOTracingProcessAndWait(t *testing.T) {
	stopErr := errors.New("permission denied")
	tests := []struct {
		name          string
		killErr       error
		done          <-chan error
		timeout       time.Duration
		expectedError string
	}{
		{
			name:    "killed",
			done:    completedProcess(),
			timeout: time.Second,
		},
		{
			name:    "already exited",
			killErr: os.ErrProcessDone,
			done:    completedProcess(),
			timeout: time.Second,
		},
		{
			name:          "kill failed",
			killErr:       stopErr,
			done:          make(chan error),
			timeout:       time.Second,
			expectedError: stopErr.Error(),
		},
		{
			name:          "stop timed out",
			done:          make(chan error),
			timeout:       time.Millisecond,
			expectedError: "timed out waiting for iotracing to stop",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			process := &processKillerStub{err: test.killErr}

			err := killIOTracingProcessAndWait(process, test.done, test.timeout)
			if test.expectedError == "" {
				if err != nil {
					t.Fatalf("killIOTracingProcessAndWait() error = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.expectedError) {
				t.Fatalf(
					"killIOTracingProcessAndWait() error = %v, want substring %q",
					err,
					test.expectedError,
				)
			}
			if !process.isKilled {
				t.Fatal("killIOTracingProcessAndWait() did not kill the process")
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

type processKillerStub struct {
	err      error
	isKilled bool
}

func (p *processKillerStub) Kill() error {
	p.isKilled = true
	return p.err
}

func completedProcess() <-chan error {
	done := make(chan error, 1)
	done <- nil
	return done
}
