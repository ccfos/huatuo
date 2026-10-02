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
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	"github.com/ccfos/huatuo/pkg/types"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

func TestNewDocumentWriterRequiresConcreteDependencies(t *testing.T) {
	if _, err := NewDocumentWriter(nil, nil); err == nil {
		t.Fatal("NewDocumentWriter(nil, nil) error = nil")
	}
}

func TestDocumentWriterRequiresSession(t *testing.T) {
	writer, err := NewDocumentWriter(
		&profilingstore.Store{},
		document.New("test"),
	)
	if err != nil {
		t.Fatalf("NewDocumentWriter() error = %v", err)
	}
	window := validProfilingWindow()

	if err := writer.Write(nil, window); err == nil {
		t.Fatal("DocumentWriter.Write() error = nil")
	} else if !strings.Contains(err.Error(), "session is required") {
		t.Fatalf("DocumentWriter.Write() error = %q", err)
	}
	if err := writer.Write(&toolstream.Session{}, window); err == nil {
		t.Fatal("DocumentWriter.Write() error = nil")
	} else if !strings.Contains(err.Error(), "session is required") {
		t.Fatalf("DocumentWriter.Write() error = %q", err)
	}
	if err := writer.Write(&toolstream.Session{Session: &transport.Session{}}, window); err == nil {
		t.Fatal("DocumentWriter.Write() error = nil")
	} else if !strings.Contains(err.Error(), "session task id is required") {
		t.Fatalf("DocumentWriter.Write() error = %q", err)
	}
}

func TestDocumentWriterPersistsJobSessionSynchronously(t *testing.T) {
	writer, backend := newPersistentDocumentWriter(t)
	session := &toolstream.Session{
		Session:    &transport.Session{TaskID: "profile-task-1"},
		IsExpected: true,
	}

	if err := writer.Write(session, validProfilingWindow()); err != nil {
		t.Fatalf("DocumentWriter.Write() error = %v", err)
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
	if got := backend.lastRecord.Fields[types.DocumentFieldTracerName]; got != types.ProfilingToolName {
		t.Fatalf("document tracer name = %q, want %q", got, types.ProfilingToolName)
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
	if got := document.ProfileData.Metrics.AggrOverflowCount; got != 7 {
		t.Fatalf("document aggregation overflow count = %d, want 7", got)
	}
}

func TestDocumentWriterPersistsNonJobSessionAsynchronously(t *testing.T) {
	writer, backend := newPersistentDocumentWriter(t)
	session := &toolstream.Session{
		Session: &transport.Session{TaskID: "profile-task-1"},
	}

	for i := range 2 {
		window := validProfilingWindow()
		window.Profile.TimeNanos += int64(time.Duration(i) * time.Minute)
		if err := writer.Write(session, window); err != nil {
			t.Fatalf("DocumentWriter.Write() error = %v", err)
		}
	}
	if backend.syncWrites != 0 || backend.asyncWrites != 2 {
		t.Fatalf(
			"profile writes = (sync=%d, async=%d), want (0, 2)",
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
			session := &toolstream.Session{
				Session: &transport.Session{TaskID: "profile-task-1"},
			}
			window := validProfilingWindow()
			test.modify(window)

			err := writer.Write(session, window)
			if err == nil {
				t.Fatal("DocumentWriter.Write() error = nil")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DocumentWriter.Write() error = %q, want containing %q", err, test.want)
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
		AggregationOverflowCount: 7,
	}
}

type profileBackend struct {
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
	if options.WaitForVisibility {
		b.syncWrites++
	} else {
		b.asyncWrites++
	}
	b.lastRecord = record
	return nil
}

func (*profileBackend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrNotFound
}

func (*profileBackend) Delete(context.Context, string) error { return nil }

func (*profileBackend) DeleteByQuery(context.Context, driver.DeleteQuery) (int64, error) {
	return 0, nil
}

func (*profileBackend) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, nil
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
