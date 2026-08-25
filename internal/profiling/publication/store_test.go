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
	"reflect"
	"testing"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
)

type memoryBackend struct {
	records          map[string]driver.Record
	syncSaves        int
	deletes          []string
	deleteByQueryErr error
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

func (b *memoryBackend) Query(_ context.Context, query driver.Query) ([]driver.Record, error) {
	records := make([]driver.Record, 0)
	for _, record := range b.records {
		if !recordMatches(record, query.Filters) {
			continue
		}
		records = append(records, record)
		if query.Limit > 0 && len(records) == query.Limit {
			break
		}
	}
	return records, nil
}

func (b *memoryBackend) DeleteByQuery(
	_ context.Context,
	query driver.Query,
) (int64, error) {
	if b.deleteByQueryErr != nil {
		return 0, b.deleteByQueryErr
	}
	var deleted int64
	for id, record := range b.records {
		if recordMatches(record, query.Filters) {
			delete(b.records, id)
			deleted++
		}
	}
	return deleted, nil
}

func (b *memoryBackend) Count(context.Context, driver.Query) (int64, error) {
	return int64(len(b.records)), nil
}

func (*memoryBackend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, nil
}

func (*memoryBackend) Close(context.Context) error { return nil }

func recordMatches(record driver.Record, filters []driver.Filter) bool {
	for _, filter := range filters {
		if filter.Op != driver.OpEq || !reflect.DeepEqual(record.Fields[filter.Field], filter.Value) {
			return false
		}
	}
	return true
}

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
	backend.records["profile-1"] = driver.Record{
		ID:     "profile-1",
		Fields: map[string]any{profileRequestIDField: "job-1"},
	}

	if err := store.Prepare(t.Context(), "job-1"); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, exists := backend.records["profile-1"]; exists {
		t.Fatal("Prepare() retained a stale profile")
	}
	published, err := store.IsPublished(t.Context(), "job-1")
	if err != nil || published {
		t.Fatalf("IsPublished() while staging = (%t, %v)", published, err)
	}
	if err := store.Publish(t.Context(), "job-1"); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if backend.syncSaves != 2 {
		t.Fatalf("SaveSync() calls = %d, want 2", backend.syncSaves)
	}
	published, err = store.IsPublished(t.Context(), "job-1")
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
	if backend.syncSaves != 3 {
		t.Fatalf("SaveSync() calls after Discard = %d, want 3", backend.syncSaves)
	}
	published, err = store.IsPublished(t.Context(), "job-1")
	if err != nil || published {
		t.Fatalf("IsPublished() after Discard = (%t, %v)", published, err)
	}
}

func TestStoreRetainsStagingMarkerWhenArtifactCleanupFails(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	backend := &memoryBackend{deleteByQueryErr: cleanupErr}
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
	store := &Store{store: markerStore, now: func() time.Time { return time.Now().UTC() }}

	err = store.Discard(t.Context(), "job-1")
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("Discard() error = %v, want %v", err, cleanupErr)
	}
	published, err := store.IsPublished(t.Context(), "job-1")
	if err != nil || published {
		t.Fatalf("IsPublished() = (%t, %v), want false, nil", published, err)
	}
	if _, err := markerStore.Get(t.Context(), markerID("job-1")); err != nil {
		t.Fatalf("Get(staging marker) error = %v", err)
	}
}

func TestStoreRecoversStagingArtifacts(t *testing.T) {
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
	store := &Store{store: markerStore, now: func() time.Time { return time.Now().UTC() }}
	if err := store.Prepare(t.Context(), "job-staging"); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	backend.records["profile-staging"] = driver.Record{
		ID:     "profile-staging",
		Fields: map[string]any{profileRequestIDField: "job-staging"},
	}
	if err := store.Prepare(t.Context(), "job-published"); err != nil {
		t.Fatalf("Prepare(published) error = %v", err)
	}
	if err := store.Publish(t.Context(), "job-published"); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	if err := store.RecoverStaging(t.Context()); err != nil {
		t.Fatalf("RecoverStaging() error = %v", err)
	}
	if _, exists := backend.records["profile-staging"]; exists {
		t.Fatal("RecoverStaging() retained a partial profile")
	}
	if _, err := markerStore.Get(t.Context(), markerID("job-staging")); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("Get(staging marker) error = %v, want ErrNotFound", err)
	}
	published, err := store.IsPublished(t.Context(), "job-published")
	if err != nil || !published {
		t.Fatalf("IsPublished(published) = (%t, %v)", published, err)
	}
}

func TestStoreReadsLegacyPublishedMarker(t *testing.T) {
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
	store := &Store{store: markerStore, now: func() time.Time { return time.Now().UTC() }}
	if err := markerStore.SaveSync(t.Context(), &Marker{
		RequestID:   "job-legacy",
		PublishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveSync(legacy marker) error = %v", err)
	}

	published, err := store.IsPublished(t.Context(), "job-legacy")
	if err != nil || !published {
		t.Fatalf("IsPublished(legacy) = (%t, %v)", published, err)
	}
}

func TestStoreRejectsEmptyRequestID(t *testing.T) {
	if err := validateRequestID(""); err == nil {
		t.Fatal("validateRequestID() error = nil")
	}
}
