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

package publication

import (
	"context"
	"testing"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
)

type memoryBackend struct {
	records   map[string]driver.Record
	syncSaves int
	deletes   []string
}

func (b *memoryBackend) Init(context.Context, string, []driver.Index) error {
	if b.records == nil {
		b.records = make(map[string]driver.Record)
	}
	return nil
}

func (b *memoryBackend) Save(_ context.Context, record driver.Record) error {
	b.records[record.ID] = record
	return nil
}

func (b *memoryBackend) SaveSync(ctx context.Context, record driver.Record) error {
	b.syncSaves++
	return b.Save(ctx, record)
}

func (b *memoryBackend) Get(_ context.Context, id string) (driver.Record, error) {
	record, ok := b.records[id]
	if !ok {
		return driver.Record{}, driver.ErrNotFound
	}
	return record, nil
}

func (b *memoryBackend) Delete(_ context.Context, id string) error {
	b.deletes = append(b.deletes, id)
	delete(b.records, id)
	return nil
}

func (*memoryBackend) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, nil
}

func (b *memoryBackend) Count(context.Context, driver.Query) (int64, error) {
	return int64(len(b.records)), nil
}

func (*memoryBackend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, nil
}

func (*memoryBackend) Close(context.Context) error { return nil }

func TestStorePublishesOnlyThroughSynchronousCommitMarker(t *testing.T) {
	backend := &memoryBackend{}
	markerStore, err := storage.NewStore[*Marker](
		t.Context(),
		"memory",
		backend,
		Collection,
		markerMapper{},
	)
	if err != nil {
		t.Fatalf("storage.NewStore() error = %v", err)
	}
	publishedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	store := &Store{store: markerStore, now: func() time.Time { return publishedAt }}

	if err := store.Prepare(t.Context(), "job-1"); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if err := store.Publish(t.Context(), "job-1"); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if backend.syncSaves != 1 {
		t.Fatalf("SaveSync() calls = %d, want 1", backend.syncSaves)
	}
	published, err := store.IsPublished(t.Context(), "job-1")
	if err != nil || !published {
		t.Fatalf("IsPublished() = (%t, %v)", published, err)
	}
	marker, err := markerStore.Get(t.Context(), markerID("job-1"))
	if err != nil {
		t.Fatalf("Get(marker) error = %v", err)
	}
	if marker.RequestID != "job-1" || !marker.PublishedAt.Equal(publishedAt) {
		t.Fatalf("marker = %+v", marker)
	}

	if err := store.Discard(t.Context(), "job-1"); err != nil {
		t.Fatalf("Discard() error = %v", err)
	}
	published, err = store.IsPublished(t.Context(), "job-1")
	if err != nil || published {
		t.Fatalf("IsPublished() after Discard = (%t, %v)", published, err)
	}
}

func TestStoreRejectsEmptyRequestID(t *testing.T) {
	if err := validateRequestID(""); err == nil {
		t.Fatal("validateRequestID() error = nil")
	}
}
