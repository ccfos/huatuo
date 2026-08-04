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
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/storage/localfile"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
	"github.com/ccfos/huatuo/internal/tracing"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"

)

// A blocking backend observes the real writer path before persistence completes.
type blockingIOTracingBackend struct {
	driver.Backend
	started chan driver.Record
	release chan struct{}
	saveErr error
}

func (*blockingIOTracingBackend) Init(context.Context, string, []driver.Index) error {
	return nil
}

func (b *blockingIOTracingBackend) Save(
	ctx context.Context,
	record driver.Record,
	_ driver.SaveOptions,
) error {
	b.started <- record
	select {
	case <-b.release:
		return b.saveErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*blockingIOTracingBackend) Close(context.Context) error { return nil }

func TestHandleIOTracingEventCompletesAfterSave(t *testing.T) {
	exitErr := errors.New("child failed")
	saveErr := errors.New("save failed")
	for _, test := range []struct {
		name    string
		exitErr error
		saveErr error
	}{
		{"success", nil, nil},
		{"failed child keeps partial report", exitErr, nil},
		{"save failure", nil, saveErr},
		{"child and save failure", exitErr, saveErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			taskID := t.Name()
			backend := &blockingIOTracingBackend{
				started: make(chan driver.Record, 1),
				release: make(chan struct{}),
				saveErr: test.saveErr,
			}
			driver.RegisterBackend("localfile", func(*driver.Config) (driver.Backend, error) {
				return backend, nil
			})
			t.Cleanup(func() {
				driver.RegisterBackend("localfile", func(cfg *driver.Config) (driver.Backend, error) {
					return localfile.NewBackend(cfg.LocalFilePath, cfg.LocalFileRotationSize, cfg.LocalFileMaxRotation), nil
				})
			})
			store, err := tracingstore.NewFromConfig(t.Context(), tracingstore.Config{
				LocalFile: &tracingstore.LocalFileConfig{
					Path: t.TempDir(), RotationSizeMiB: 1, MaxRotatedFiles: 1,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				tracing.DisableDocumentWriter()
				if err := store.Close(context.Background()); err != nil {
					t.Errorf("close tracing store: %v", err)
				}
			})
			if err := tracing.EnableDocumentWriter(store, document.New("test-region")); err != nil {
				t.Fatal(err)
			}
			pending := &pendingIOTracingReason{
				reason: &reasonSnapshot{
					Type: string(ioReasonUtil), Device: "sda", IOStatus: diskStatus{IOUtil: 95},
				},
				startedTimestamp: timeutil.Now(),
				received:         make(chan struct{}),
				result:           make(chan error, 1),
			}
			pendingReasons.Store(taskID, pending)
			report := &types.IOTracingSnapshot{
				Processes: []types.ProcessFileIOStats{{PID: 42, Comm: "writer", TotalDiskWriteBps: 4096}},
				StallStacks: []types.IOScheduleEvent{{
					PID: 42, TID: 43, CPU: 2, Comm: "writer", ScheduleLatencyUS: 2500,
					ContainerHostname: "container", Stack: []string{"io_schedule"},
				}},
			}
			if test.exitErr != nil {
				report.FailureReason = types.IOTracingFailureReader
			}
			waitDone := make(chan error, 1)
			go func() {
				waitDone <- waitForSnapshotAfterExit(t.Context(), taskID, pending, test.exitErr, time.Second, time.Second)
			}()
			select {
			case err := <-waitDone:
				t.Fatalf("wait returned before snapshot delivery: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
			if value, ok := pendingReasons.Load(taskID); !ok || value != pending {
				t.Fatal("child exit did not retain its pending reason")
			}
			handlerDone := make(chan error, 1)
			handlerStopped := make(chan struct{})
			go func() {
				defer close(handlerStopped)
				handlerDone <- handleIotracingEvent(
					&toolstream.Session{Session: &transport.Session{TaskID: taskID}}, report)
			}()
			t.Cleanup(func() {
				select {
				case <-backend.release:
				default:
					close(backend.release)
				}
				select {
				case <-handlerStopped:
				case <-time.After(time.Second):
					t.Error("handler did not stop after releasing save")
				}
				pendingReasons.Delete(taskID)
			})
			var record driver.Record
			select {
			case record = <-backend.started:
			case <-time.After(time.Second):
				t.Fatal("save did not start")
			}
			var document struct {
				TracerName       string             `json:"tracer_name"`
				TracerRunType    string             `json:"tracer_type"`
				StartedTimestamp timeutil.Timestamp `json:"started_timestamp"`
				TracerData       ioStatusData       `json:"tracer_data"`
			}
			if err := json.Unmarshal(record.Data, &document); err != nil {
				t.Fatal(err)
			}
			if document.TracerName != iotracingToolName ||
				document.TracerRunType != types.TracerRunTypeAutotracing ||
				!document.StartedTimestamp.Equal(pending.startedTimestamp.Time) {
				t.Fatalf("saved document metadata = %+v", document)
			}
			wantData := ioStatusData{
				Reason: pending.reason, FailureReason: report.FailureReason,
				Processes: report.Processes, StallStacks: report.StallStacks,
			}
			if !reflect.DeepEqual(document.TracerData, wantData) {
				t.Fatalf("saved snapshot = %+v, want %+v", document.TracerData, wantData)
			}
			if _, ok := pendingReasons.Load(taskID); ok {
				t.Fatal("handler retained the delivered pending reason")
			}
			select {
			case <-pending.received:
			default:
				t.Fatal("handler did not signal snapshot receipt before save")
			}
			select {
			case err := <-waitDone:
				t.Fatalf("snapshot wait completed before save returned: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
			close(backend.release)
			select {
			case err := <-handlerDone:
				if !errors.Is(err, test.saveErr) {
					t.Fatalf("handler error = %v, want %v", err, test.saveErr)
				}
			case <-time.After(time.Second):
				t.Fatal("handler did not finish after save returned")
			}
			select {
			case err := <-waitDone:
				if (test.exitErr == nil && test.saveErr == nil && err != nil) ||
					(test.exitErr != nil && !errors.Is(err, test.exitErr)) ||
					(test.saveErr != nil && !errors.Is(err, test.saveErr)) {
					t.Fatalf("wait error = %v, want child %v and save %v", err, test.exitErr, test.saveErr)
				}
			case <-time.After(time.Second):
				t.Fatal("snapshot wait did not finish after save returned")
			}
		})
	}
}

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

func TestWaitForSnapshotAfterFailedExitCancellation(t *testing.T) {
	const taskID = "canceled-failed-child-task"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pending := &pendingIOTracingReason{
		received: make(chan struct{}),
		result:   make(chan error, 1),
	}
	pendingReasons.Store(taskID, pending)
	t.Cleanup(func() { pendingReasons.Delete(taskID) })
	cancel()
	err := waitForSnapshotAfterExit(ctx, taskID, pending, errors.New("child failed"), time.Second, time.Second)
	if err != nil {
		t.Fatalf("canceled snapshot wait returned a child error: %v", err)
	}
	if _, ok := pendingReasons.Load(taskID); ok {
		t.Fatal("canceled snapshot wait retained its pending reason")
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
