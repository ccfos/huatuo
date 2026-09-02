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

package kafka

import (
	"context"
	"errors"
	"testing"

	"huatuo-bamai/internal/storage/driver"
)

type fakeProducer struct {
	topic  string
	msg    message
	err    error
	closed bool
}

func (p *fakeProducer) Write(_ context.Context, topic string, msg message) error {
	p.topic = topic
	p.msg = msg
	return p.err
}

func (p *fakeProducer) Close() { p.closed = true }

func TestStorageSave(t *testing.T) {
	producer := &fakeProducer{}
	backend := newStorage(producer, "huatuo-events")
	if err := backend.Init(t.Context(), "tracing_documents", nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	err := backend.Save(t.Context(), driver.Record{
		ID:   "trace-20260809",
		Data: []byte(`{"tracer_name":"oom"}`),
		Fields: map[string]any{
			"tracer_name": "oom",
			"region":      "cn-beijing",
			"ignored":     42,
		},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if producer.topic != "huatuo-events" {
		t.Fatalf("topic = %q, want huatuo-events", producer.topic)
	}
	if got := string(producer.msg.key); got != "trace-20260809" {
		t.Errorf("key = %q, want trace-20260809", got)
	}
	if got := string(producer.msg.value); got != `{"tracer_name":"oom"}` {
		t.Errorf("value = %q", got)
	}

	wantHeaders := map[string]string{
		"content-type":       contentTypeJSON,
		"huatuo-collection":  "tracing_documents",
		"huatuo-region":      "cn-beijing",
		"huatuo-tracer-name": "oom",
	}
	for _, h := range producer.msg.headers {
		if want, ok := wantHeaders[h.key]; ok {
			if got := string(h.value); got != want {
				t.Errorf("header %q = %q, want %q", h.key, got, want)
			}
			delete(wantHeaders, h.key)
		}
	}
	if len(wantHeaders) != 0 {
		t.Errorf("missing headers: %v", wantHeaders)
	}
}

func TestStorageWriteErrorAndUnsupportedOperations(t *testing.T) {
	wantErr := errors.New("broker unavailable")
	producer := &fakeProducer{err: wantErr}
	backend := newStorage(producer, "huatuo-events")
	if err := backend.Init(t.Context(), "tracing_documents", nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	err := backend.Save(t.Context(), driver.Record{ID: "trace", Data: []byte(`{}`)})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Save() error = %v, want wrap %v", err, wantErr)
	}
	if _, err := backend.Get(t.Context(), "trace"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Get() error = %v, want ErrUnsupported", err)
	}
	if err := backend.Delete(t.Context(), "trace"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("Delete() error = %v, want ErrUnsupported", err)
	}
	if err := backend.Close(t.Context()); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	if !producer.closed {
		t.Error("Close() did not close producer")
	}
}

func TestStorageRejectsInvalidState(t *testing.T) {
	backend := newStorage(&fakeProducer{}, "huatuo-events")
	if err := backend.Init(t.Context(), "", nil); err == nil {
		t.Fatal("Init() error = nil, want empty collection error")
	}
	if err := backend.Save(t.Context(), driver.Record{ID: "trace"}); err == nil {
		t.Fatal("Save() error = nil, want uninitialized error")
	}
	if err := backend.Init(t.Context(), "tracing_documents", nil); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := backend.Save(t.Context(), driver.Record{}); !errors.Is(err, driver.ErrInvalidField) {
		t.Fatalf("Save() error = %v, want ErrInvalidField", err)
	}
}
