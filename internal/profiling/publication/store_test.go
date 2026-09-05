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
	"errors"
	"testing"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
)

type memoryBackend struct {
	records     map[string]driver.Record
	syncSaveErr error
	syncSaves   int
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
	if b.syncSaveErr != nil {
		return b.syncSaveErr
	}
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

func newTestStore(
	t *testing.T,
	backend *memoryBackend,
	now func() time.Time,
) (*Store, *storage.Store[*Marker]) {
	t.Helper()
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
	return &Store{store: markerStore, now: now}, markerStore
}

func TestStorePublishesSynchronousCommitMarker(t *testing.T) {
	backend := &memoryBackend{}
	publishedAt := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	store, markerStore := newTestStore(t, backend, func() time.Time { return publishedAt })

	published, err := store.IsPublished(t.Context(), "job-1")
	if err != nil || published {
		t.Fatalf("IsPublished() before Publish = (%t, %v), want (false, nil)", published, err)
	}
	if err := store.Publish(t.Context(), "job-1"); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if backend.syncSaves != 1 {
		t.Fatalf("SaveSync() calls = %d, want 1", backend.syncSaves)
	}
	published, err = store.IsPublished(t.Context(), "job-1")
	if err != nil || !published {
		t.Fatalf("IsPublished() after Publish = (%t, %v), want (true, nil)", published, err)
	}
	marker, err := markerStore.Get(t.Context(), markerID("job-1"))
	if err != nil {
		t.Fatalf("Get(marker) error = %v", err)
	}
	if marker.RequestID != "job-1" || !marker.PublishedAt.Equal(publishedAt) {
		t.Fatalf("marker = %+v", marker)
	}
}

func TestStorePublishIsIdempotentByRequestID(t *testing.T) {
	backend := &memoryBackend{}
	store, _ := newTestStore(t, backend, func() time.Time { return time.Now().UTC() })

	if err := store.Publish(t.Context(), "job-1"); err != nil {
		t.Fatalf("first Publish() error = %v", err)
	}
	if err := store.Publish(t.Context(), "job-1"); err != nil {
		t.Fatalf("second Publish() error = %v", err)
	}
	if backend.syncSaves != 2 || len(backend.records) != 1 {
		t.Fatalf(
			"Publish() result = (sync saves=%d, records=%d), want (2, 1)",
			backend.syncSaves,
			len(backend.records),
		)
	}
}

func TestStoreTreatsLegacyStagingMarkerAsUnpublished(t *testing.T) {
	backend := &memoryBackend{}
	store, markerStore := newTestStore(t, backend, func() time.Time { return time.Now().UTC() })
	if err := markerStore.SaveSync(t.Context(), &Marker{RequestID: "job-legacy"}); err != nil {
		t.Fatalf("SaveSync(legacy marker) error = %v", err)
	}

	published, err := store.IsPublished(t.Context(), "job-legacy")
	if err != nil || published {
		t.Fatalf("IsPublished(legacy) = (%t, %v), want (false, nil)", published, err)
	}
}

func TestStoreReturnsPublishFailure(t *testing.T) {
	saveErr := errors.New("save failed")
	backend := &memoryBackend{syncSaveErr: saveErr}
	store, _ := newTestStore(t, backend, func() time.Time { return time.Now().UTC() })

	if err := store.Publish(t.Context(), "job-1"); !errors.Is(err, saveErr) {
		t.Fatalf("Publish() error = %v, want %v", err, saveErr)
	}
}
