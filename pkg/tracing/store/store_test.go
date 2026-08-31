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

package store

import (
	"context"
	"testing"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/types"
)

type testBackend struct {
	saved []driver.Record
}

func (*testBackend) Init(context.Context, string, []driver.Index) error { return nil }

func (b *testBackend) Save(_ context.Context, record driver.Record) error {
	b.saved = append(b.saved, record)
	return nil
}

func (*testBackend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrNotFound
}

func (*testBackend) Delete(context.Context, string) error { return nil }

func (*testBackend) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, nil
}

func (*testBackend) Count(context.Context, driver.Query) (int64, error) { return 0, nil }

func (*testBackend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, nil
}

func (*testBackend) Close(context.Context) error { return nil }

func TestStoreSavesAndPublishesTracingDocument(t *testing.T) {
	backend := &testBackend{}
	persistence, err := storage.NewStore[*Document](
		t.Context(),
		"memory",
		backend,
		Collection,
		Mapper{},
	)
	if err != nil {
		t.Fatalf("storage.NewStore() error = %v", err)
	}
	store := New([]*storage.Store[*Document]{persistence})
	documents, cancel := store.Subscribe()
	defer cancel()
	observedTimestamp := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	document := &Document{
		Document: types.Document{
			Hostname:          "node-1",
			TracerID:          "trace-1",
			TracerRunType:     types.TracerRunTypeEvent,
			ObservedTimestamp: &observedTimestamp,
		},
		TracerData: map[string]any{"value": float64(1)},
	}

	if err := store.Save(document); err != nil {
		t.Fatalf("Store.Save() error = %v", err)
	}
	if len(backend.saved) != 1 {
		t.Fatalf("backend saves = %d, want 1", len(backend.saved))
	}
	if document.UploadedTimestamp.IsZero() {
		t.Fatal("document uploaded timestamp is zero")
	}
	select {
	case got := <-documents:
		if got != document {
			t.Fatalf("subscriber document = %p, want %p", got, document)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive tracing document")
	}
}

func TestMapperKeepsCommonFieldsFlat(t *testing.T) {
	observedTimestamp := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	document := &Document{
		Document: types.Document{
			Hostname:          "node-1",
			TracerID:          "trace-1",
			TracerRunType:     types.TracerRunTypeEvent,
			UploadedTimestamp: time.Date(2026, 8, 28, 2, 0, 1, 0, time.UTC),
			ObservedTimestamp: &observedTimestamp,
		},
		TracerData: map[string]any{"kind": "drop"},
	}
	encoded, err := (Mapper{}).Encode(document)
	if err != nil {
		t.Fatalf("Mapper.Encode() error = %v", err)
	}
	decoded, err := (Mapper{}).Decode(encoded)
	if err != nil {
		t.Fatalf("Mapper.Decode() error = %v", err)
	}
	if decoded.Hostname != "node-1" || decoded.TracerID != "trace-1" {
		t.Fatalf("decoded document = %+v", decoded)
	}
}
