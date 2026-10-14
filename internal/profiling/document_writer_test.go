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

package profiling

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/toolstream"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	"github.com/ccfos/huatuo/pkg/types"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

func TestNewDocumentWriterRequiresConcreteDependencies(t *testing.T) {
	if _, err := NewDocumentWriter(nil, nil); err == nil {
		t.Fatal("NewDocumentWriter(nil, nil) error = nil")
	}
}

func TestDocumentWriterRequiresRequest(t *testing.T) {
	writer, _ := newPersistentDocumentWriter(t)
	if err := writer.Handle(t.Context(), nil); err == nil {
		t.Fatal("nil request accepted")
	}
	if err := writer.Handle(t.Context(), &toolstream.Request[types.ProfilingWindow]{}); err == nil {
		t.Fatal("empty task ID accepted")
	}
}

func TestDocumentWriterPersistsWindowSynchronously(t *testing.T) {
	const toolName = "custom-profiler"
	writer, backend := newPersistentDocumentWriter(t)
	session := &toolstream.Request[types.ProfilingWindow]{ToolName: toolName, TaskID: "profile-task-1"}

	session.Data = validProfilingWindow()
	if err := writer.Handle(t.Context(), session); err != nil {
		t.Fatalf("DocumentWriter.Handle() error = %v", err)
	}
	if backend.syncWrites != 1 || backend.asyncWrites != 0 {
		t.Fatalf(
			"profile writes = (sync=%d, async=%d), want (1, 0)",
			backend.syncWrites,
			backend.asyncWrites,
		)
	}
	if got := backend.lastRecord.Fields[types.DocumentFieldTracerID]; got != "profile-task-1" {
		t.Fatalf("document tracer ID = %q, want %q", got, "profile-task-1")
	}
	if got := backend.lastRecord.Fields[types.DocumentFieldTracerName]; got != toolName {
		t.Fatalf("document tracer name = %q, want %q", got, toolName)
	}
	if got := backend.lastRecord.Fields[types.DocumentFieldTracerType]; got != types.TracerRunTypeProfiling {
		t.Fatalf("document tracer type = %q, want %q", got, types.TracerRunTypeProfiling)
	}
	var document profilingstore.Document
	if err := json.Unmarshal(backend.lastRecord.Data, &document); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := document.ProfileData.ProfileType; got != "process_cpu:cpu:nanoseconds:cpu:nanoseconds" {
		t.Fatalf("document profile type = %q", got)
	}
	if got := document.ProfileData.Metrics.DroppedSamples[types.DropReasonAggregationQueue]; got != 7 {
		t.Fatalf("document dropped sample count = %d, want 7", got)
	}
}

func TestDocumentWriterPersistsEveryWindowSynchronously(t *testing.T) {
	writer, backend := newPersistentDocumentWriter(t)
	session := &toolstream.Request[types.ProfilingWindow]{ToolName: "profiler", TaskID: "profile-task-1"}

	for i := range 2 {
		window := validProfilingWindow()
		window.Profile.TimeNanos += int64(time.Duration(i) * time.Minute)
		session.Data = window
		if err := writer.Handle(t.Context(), session); err != nil {
			t.Fatalf("DocumentWriter.Handle() error = %v", err)
		}
	}
	if backend.syncWrites != 2 || backend.asyncWrites != 0 {
		t.Fatalf(
			"profile writes = (sync=%d, async=%d), want (2, 0)",
			backend.syncWrites,
			backend.asyncWrites,
		)
	}
}

func TestDocumentWriterRejectsIncompleteWindow(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*types.ProfilingWindow)
		want   string
	}{
		{
			name: "missing profile type",
			modify: func(window *types.ProfilingWindow) {
				window.ProfileType = ""
			},
			want: "profile type is required",
		},
		{
			name: "missing profile",
			modify: func(window *types.ProfilingWindow) {
				window.Profile = nil
			},
			want: "profile is required",
		},
		{
			name: "missing profile start timestamp",
			modify: func(window *types.ProfilingWindow) {
				window.Profile.TimeNanos = 0
			},
			want: "profile start timestamp is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer, backend := newPersistentDocumentWriter(t)
			session := &toolstream.Request[types.ProfilingWindow]{ToolName: "profiler", TaskID: "profile-task-1"}
			window := validProfilingWindow()
			test.modify(window)

			session.Data = window
			err := writer.Handle(t.Context(), session)
			if err == nil {
				t.Fatal("DocumentWriter.Handle() error = nil")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DocumentWriter.Handle() error = %q, want containing %q", err, test.want)
			}
			if backend.syncWrites != 0 || backend.asyncWrites != 0 {
				t.Fatalf(
					"profile writes = (sync=%d, async=%d), want (0, 0)",
					backend.syncWrites,
					backend.asyncWrites,
				)
			}
		})
	}
}

func validProfilingWindow() *types.ProfilingWindow {
	return &types.ProfilingWindow{
		ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		Profile: &profilev1.Profile{
			TimeNanos: time.Date(2026, 8, 28, 2, 30, 0, 0, time.UTC).UnixNano(),
		},
		DroppedSamples: map[string]uint64{types.DropReasonAggregationQueue: 7},
	}
}

type profileBackend struct {
	saveErr     error
	commitErr   error
	commits     int
	asyncWrites int
	syncWrites  int
	lastRecord  driver.Record
}

func (*profileBackend) Init(context.Context, string, []driver.Index) error { return nil }

func (b *profileBackend) Save(
	_ context.Context,
	record driver.Record,
	options driver.SaveOptions,
) error {
	if strings.HasPrefix(record.ID, "profiling-publication:") {
		b.commits++
		return b.commitErr
	}
	if options.WaitForVisibility {
		b.syncWrites++
	} else {
		b.asyncWrites++
	}
	b.lastRecord = record
	return b.saveErr
}

func (*profileBackend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrNotFound
}

func (*profileBackend) Delete(context.Context, string) error { return nil }

func (*profileBackend) DeleteByQuery(context.Context, driver.DeleteQuery) (int64, error) {
	return 0, nil
}

func (*profileBackend) Query(context.Context, driver.Query, func([]driver.Record) error) error {
	return nil
}

func (*profileBackend) Count(context.Context, driver.Query) (int64, error) { return 0, nil }

func (*profileBackend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, nil
}

func (*profileBackend) Close(context.Context) error { return nil }

func newPersistentDocumentWriter(t *testing.T) (*DocumentWriter, *profileBackend) {
	t.Helper()
	backend := &profileBackend{}
	driver.RegisterBackend("elasticsearch", func(*driver.Config) (driver.Backend, error) {
		return backend, nil
	})

	store, err := profilingstore.NewFromConfig(t.Context(), profilingstore.Config{
		Index: "profiling-results-test",
	})
	if err != nil {
		t.Fatalf("profilingstore.NewFromConfig() error = %v", err)
	}
	writer, err := NewDocumentWriter(
		store,
		document.New("test"),
	)
	if err != nil {
		t.Fatalf("NewDocumentWriter() error = %v", err)
	}

	return writer, backend
}

func TestHandleStoresFinalWindowBeforeConditionalCommit(t *testing.T) {
	for _, failure := range []string{"", "prior", "current", "write", "commit", "missing-commit"} {
		t.Run(failure, func(t *testing.T) {
			writer, backend := newPersistentDocumentWriter(t)
			request := &toolstream.Request[types.ProfilingWindow]{ToolName: "profiler", TaskID: "task", Data: validProfilingWindow(), End: true, Commit: true}
			sentinel := errors.New("storage failure")
			switch failure {
			case "prior":
				request.PriorErr = sentinel
			case "current":
				request.Err = sentinel
			case "write":
				backend.saveErr = sentinel
			case "commit":
				backend.commitErr = sentinel
			case "missing-commit":
				request.Commit = false
			}
			err := writer.Handle(t.Context(), request)
			wantErr := failure == "write" || failure == "commit" || failure == "missing-commit"
			if (err != nil) != wantErr {
				t.Fatalf("Handle: %v", err)
			}
			if backend.syncWrites != 1 {
				t.Fatalf("window writes=%d", backend.syncWrites)
			}
			wantCommits := 0
			if failure == "" || failure == "commit" {
				wantCommits = 1
			}
			if backend.commits != wantCommits {
				t.Fatalf("commits=%d want=%d", backend.commits, wantCommits)
			}
		})
	}
}

func TestDocumentWriterRequiresToolName(t *testing.T) {
	writer, backend := newPersistentDocumentWriter(t)
	request := &toolstream.Request[types.ProfilingWindow]{TaskID: "task", Data: validProfilingWindow(), End: true, Commit: true}
	err := writer.Handle(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "tool name is required") {
		t.Fatalf("Handle() error = %v", err)
	}
	if backend.syncWrites != 0 || backend.commits != 0 {
		t.Fatalf("writes=%d commits=%d", backend.syncWrites, backend.commits)
	}
}
