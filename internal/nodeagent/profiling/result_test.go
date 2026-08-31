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
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/document"
	profilingresult "huatuo-bamai/internal/profiling/result"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/toolstream/transport"
	profilingstore "huatuo-bamai/pkg/profiling/store"
	"huatuo-bamai/pkg/types"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

func TestNewResultWriterRequiresConcreteDependencies(t *testing.T) {
	if _, err := NewResultWriter(nil, nil); err == nil {
		t.Fatal("NewResultWriter() error = nil")
	}
}

func TestResultWriterRequiresSession(t *testing.T) {
	writer, err := NewResultWriter(
		&profilingstore.Store{},
		document.New("test", "test-host"),
	)
	if err != nil {
		t.Fatalf("NewResultWriter() error = %v", err)
	}
	event := validResultEvent()

	if err := writer.Write(nil, event); err == nil {
		t.Fatal("ResultWriter.Write() error = nil")
	} else if !strings.Contains(err.Error(), "session is required") {
		t.Fatalf("ResultWriter.Write() error = %q", err)
	}
	if err := writer.Write(&toolstream.Session{}, event); err == nil {
		t.Fatal("ResultWriter.Write() error = nil")
	} else if !strings.Contains(err.Error(), "session is required") {
		t.Fatalf("ResultWriter.Write() error = %q", err)
	}
}

func TestResultWriterPersistsJobSessionSynchronously(t *testing.T) {
	writer, backend := newPersistentResultWriter(t)
	session := &toolstream.Session{
		Session:    &transport.Session{TaskID: "profile-task-1"},
		IsExpected: true,
	}

	if err := writer.Write(session, validResultEvent()); err != nil {
		t.Fatalf("ResultWriter.Write() error = %v", err)
	}
	if backend.syncWrites != 1 || backend.asyncWrites != 0 {
		t.Fatalf(
			"profile writes = (sync=%d, async=%d), want (1, 0)",
			backend.syncWrites,
			backend.asyncWrites,
		)
	}
}

func TestResultWriterPersistsNonJobSessionAsynchronously(t *testing.T) {
	writer, backend := newPersistentResultWriter(t)
	session := &toolstream.Session{
		Session: &transport.Session{TaskID: "profile-task-1"},
	}

	for i := range 2 {
		event := validResultEvent()
		event.StartedTimestamp = event.StartedTimestamp.Add(time.Duration(i) * time.Minute)
		if err := writer.Write(session, event); err != nil {
			t.Fatalf("ResultWriter.Write() error = %v", err)
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

func TestResultWriterRejectsIncompleteProfile(t *testing.T) {
	writer, backend := newPersistentResultWriter(t)
	session := &toolstream.Session{
		Session: &transport.Session{TaskID: "profile-task-1"},
	}
	event := validResultEvent()
	event.ProfileData = nil

	err := writer.Write(session, event)
	if err == nil {
		t.Fatal("ResultWriter.Write() error = nil")
	}
	if !strings.Contains(err.Error(), "profile data is required") {
		t.Fatalf("ResultWriter.Write() error = %q", err)
	}
	if backend.syncWrites != 0 || backend.asyncWrites != 0 {
		t.Fatalf(
			"profile writes = (sync=%d, async=%d), want (0, 0)",
			backend.syncWrites,
			backend.asyncWrites,
		)
	}
}

func TestResultWriterRejectsMismatchedTask(t *testing.T) {
	writer, err := NewResultWriter(
		&profilingstore.Store{},
		document.New("test", "test-host"),
	)
	if err != nil {
		t.Fatalf("NewResultWriter() error = %v", err)
	}
	session := &toolstream.Session{
		Session:    &transport.Session{TaskID: "another-task"},
		IsExpected: true,
	}

	err = writer.Write(session, validResultEvent())
	if err == nil {
		t.Fatal("ResultWriter.Write() error = nil")
	}
	if !strings.Contains(err.Error(), "does not match session task id") {
		t.Fatalf("ResultWriter.Write() error = %q", err)
	}
}

func TestResultWriterRejectsNonProfilingResult(t *testing.T) {
	writer, err := NewResultWriter(
		&profilingstore.Store{},
		document.New("test", "test-host"),
	)
	if err != nil {
		t.Fatalf("NewResultWriter() error = %v", err)
	}
	session := &toolstream.Session{
		Session:    &transport.Session{TaskID: "profile-task-1"},
		IsExpected: true,
	}
	event := validResultEvent()
	event.TracerRunType = types.TracerRunTypeAutotracing

	err = writer.Write(session, event)
	if err == nil {
		t.Fatal("ResultWriter.Write() error = nil")
	}
	if !strings.Contains(err.Error(), "tracer type") {
		t.Fatalf("ResultWriter.Write() error = %q", err)
	}
}

func validResultEvent() *profilingresult.Event {
	return &profilingresult.Event{
		TracerID:         "profile-task-1",
		TracerName:       "oncpu",
		TracerRunType:    types.TracerRunTypeProfiling,
		StartedTimestamp: time.Date(2026, 8, 28, 2, 30, 0, 0, time.UTC),
		ProfileData: &profilingstore.ProfileData{
			ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			Profile:     &profilev1.Profile{},
		},
	}
}

type profileBackend struct {
	asyncWrites int
	syncWrites  int
}

func (*profileBackend) Init(context.Context, string, []driver.Index) error { return nil }

func (b *profileBackend) Save(context.Context, driver.Record) error {
	b.asyncWrites++
	return nil
}

func (b *profileBackend) SaveSync(context.Context, driver.Record) error {
	b.syncWrites++
	return nil
}

func (*profileBackend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrNotFound
}

func (*profileBackend) Delete(context.Context, string) error { return nil }

func (*profileBackend) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, nil
}

func (*profileBackend) Count(context.Context, driver.Query) (int64, error) { return 0, nil }

func (*profileBackend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, nil
}

func (*profileBackend) Close(context.Context) error { return nil }

func newPersistentResultWriter(t *testing.T) (*ResultWriter, *profileBackend) {
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
	writer, err := NewResultWriter(
		store,
		document.New("test", "test-host"),
	)
	if err != nil {
		t.Fatalf("NewResultWriter() error = %v", err)
	}

	return writer, backend
}
