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
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/nodeagent"
	profilingresult "huatuo-bamai/internal/profiling/result"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/toolstream/transport"
	profilingstore "huatuo-bamai/pkg/profiling/store"
	"huatuo-bamai/pkg/types"
)

func TestNewResultWriterRequiresConcreteDependencies(t *testing.T) {
	if _, err := NewResultWriter(nil, nil); err == nil {
		t.Fatal("NewResultWriter() error = nil")
	}
}

func TestResultWriterRejectsUnexpectedSession(t *testing.T) {
	writer, err := NewResultWriter(
		&profilingstore.Store{},
		nodeagent.NewDocumentBuilder("test", "test-host"),
	)
	if err != nil {
		t.Fatalf("NewResultWriter() error = %v", err)
	}
	event := validResultEvent()

	if err := writer.Write(nil, event); err == nil {
		t.Fatal("ResultWriter.Write() error = nil")
	} else if !strings.Contains(err.Error(), "session was not expected") {
		t.Fatalf("ResultWriter.Write() error = %q", err)
	}
	if err := writer.Write(&toolstream.Session{}, event); err == nil {
		t.Fatal("ResultWriter.Write() error = nil")
	} else if !strings.Contains(err.Error(), "session was not expected") {
		t.Fatalf("ResultWriter.Write() error = %q", err)
	}
}

func TestResultWriterRejectsMismatchedTask(t *testing.T) {
	writer, err := NewResultWriter(
		&profilingstore.Store{},
		nodeagent.NewDocumentBuilder("test", "test-host"),
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

func TestResultWriterRejectsNonTaskResult(t *testing.T) {
	writer, err := NewResultWriter(
		&profilingstore.Store{},
		nodeagent.NewDocumentBuilder("test", "test-host"),
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
	}
}
