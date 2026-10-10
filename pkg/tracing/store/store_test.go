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
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/storage"
	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/watch"
	"github.com/ccfos/huatuo/pkg/types"
)

type testBackend struct {
	saved []driver.Record
}

func (*testBackend) Init(context.Context, string, []driver.Index) error { return nil }

func (b *testBackend) Save(
	_ context.Context,
	record driver.Record,
	_ driver.SaveOptions,
) error {
	b.saved = append(b.saved, record)
	return nil
}

func (*testBackend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrNotFound
}

func (*testBackend) Delete(context.Context, string) error { return nil }

func (*testBackend) DeleteByQuery(context.Context, driver.DeleteQuery) (int64, error) {
	return 0, nil
}

func (*testBackend) Query(context.Context, driver.Query, func([]driver.Record) error) error {
	return nil
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
		mapper{},
	)
	if err != nil {
		t.Fatalf("storage.NewStore() error = %v", err)
	}
	store := &Store{
		backends: []*storage.Store[*Document]{persistence},
		hub:      watch.NewHub[*Document](),
	}
	documents, cancel := store.Subscribe()
	defer cancel()
	observedTimestamp := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	document := &Document{
		Document: types.Document{
			Hostname:          "node-1",
			TracerID:          "trace-1",
			TracerRunType:     types.TracerRunTypeEvent,
			ObservedTimestamp: &timeutil.Timestamp{Time: observedTimestamp},
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

func TestStoreRejectsInvalidDocumentBeforePersistence(t *testing.T) {
	backend := &testBackend{}
	persistence, err := storage.NewStore[*Document](
		t.Context(),
		"memory",
		backend,
		Collection,
		mapper{},
	)
	if err != nil {
		t.Fatalf("storage.NewStore() error = %v", err)
	}
	store := &Store{
		backends: []*storage.Store[*Document]{persistence},
		hub:      watch.NewHub[*Document](),
	}

	if err := store.Save(&Document{}); err == nil {
		t.Fatal("Store.Save() error = nil, want invalid document error")
	}
	if len(backend.saved) != 0 {
		t.Fatalf("backend saves = %d, want 0", len(backend.saved))
	}
}

func TestMapperKeepsCommonFieldsFlat(t *testing.T) {
	observedTimestamp := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	document := &Document{
		Document: types.Document{
			Hostname:          "node-1",
			TracerID:          "trace-1",
			TracerRunType:     types.TracerRunTypeEvent,
			UploadedTimestamp: timeutil.Timestamp{Time: time.Date(2026, 8, 28, 2, 0, 1, 0, time.UTC)},
			ObservedTimestamp: &timeutil.Timestamp{Time: observedTimestamp},
		},
		TracerData: map[string]any{"kind": "drop"},
	}
	encoded, err := (mapper{}).Encode(document)
	if err != nil {
		t.Fatalf("Mapper.Encode() error = %v", err)
	}
	decoded, err := (mapper{}).Decode(driver.Record{Data: encoded})
	if err != nil {
		t.Fatalf("Mapper.Decode() error = %v", err)
	}
	if decoded.Hostname != "node-1" || decoded.TracerID != "trace-1" {
		t.Fatalf("decoded document = %+v", decoded)
	}
}

func TestMapperKernelObservationRoundTrip(t *testing.T) {
	observed := time.Date(2026, 9, 15, 2, 0, 1, 0, time.UTC)
	kernel := observed.Add(-time.Second)
	for _, available := range []bool{false, true} {
		name := "legacy document"
		if available {
			name = "kernel observation present"
		}
		t.Run(name, func(t *testing.T) {
			doc := &Document{Document: types.Document{
				TracerRunType:     types.TracerRunTypeEvent,
				ObservedTimestamp: &timeutil.Timestamp{Time: observed},
				UploadedTimestamp: timeutil.Timestamp{Time: observed.Add(time.Second)},
			}}
			if available {
				doc.KernelObservedTimestamp = &timeutil.Timestamp{Time: kernel}
			}
			m := mapper{}
			data, err := m.Encode(doc)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatal(err)
			}
			_, present := raw[types.DocumentFieldKernelObservedTimestamp]
			if present != available {
				t.Fatalf("kernel timestamp presence = %v", present)
			}
			fields, err := m.Fields(doc)
			if err != nil {
				t.Fatal(err)
			}
			_, indexed := fields[types.DocumentFieldKernelObservedTimestamp]
			if indexed != available {
				t.Fatalf("kernel index presence = %v", indexed)
			}
			got, err := m.Decode(driver.Record{Data: data})
			if err != nil {
				t.Fatal(err)
			}
			if !got.ObservedTimestamp.Equal(observed) {
				t.Fatal("userspace timestamp changed")
			}
			if available {
				if got.KernelObservedTimestamp == nil || !got.KernelObservedTimestamp.Equal(kernel) {
					t.Fatalf("kernel timestamp = %v", got.KernelObservedTimestamp)
				}
			} else if got.KernelObservedTimestamp != nil {
				t.Fatal("legacy document gained a kernel timestamp")
			}
		})
	}
}

type contextSaveBackend struct {
	testBackend
	ctx context.Context
}

func (b *contextSaveBackend) Save(ctx context.Context, record driver.Record, options driver.SaveOptions) error {
	b.ctx = ctx
	return b.testBackend.Save(ctx, record, options)
}

func TestStoreSaveContextPassesContextToBackend(t *testing.T) {
	backend := &contextSaveBackend{}
	persistence, err := storage.NewStore[*Document](t.Context(), "memory", backend, Collection, mapper{})
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{
		backends: []*storage.Store[*Document]{persistence},
		hub:      watch.NewHub[*Document](),
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := timeutil.Now()
	document := &Document{Document: types.Document{
		TracerID:          "context-save",
		TracerRunType:     types.TracerRunTypeEvent,
		ObservedTimestamp: &observed,
	}}
	if err := store.SaveContext(ctx, document); err != nil {
		t.Fatal(err)
	}
	if backend.ctx != ctx {
		t.Fatal("save context did not reach persistence backend")
	}
	if len(backend.saved) != 1 {
		t.Fatalf("backend saves = %d, want 1", len(backend.saved))
	}
}

func TestStoreCloseDoesNotWaitForCompletedSave(t *testing.T) {
	backend := &testBackend{}
	persistence, err := storage.NewStore[*Document](t.Context(), "memory", backend, Collection, mapper{})
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{
		backends: []*storage.Store[*Document]{persistence},
		hub:      watch.NewHub[*Document](),
	}
	observed := timeutil.Now()
	if err := store.Save(&Document{Document: types.Document{
		TracerID:          "completed-save",
		TracerRunType:     types.TracerRunTypeEvent,
		ObservedTimestamp: &observed,
	}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.Close(ctx); err != nil {
		t.Fatalf("Close() waited for a completed save: %v", err)
	}
}

type blockingSaveBackend struct {
	testBackend
	started    chan struct{}
	release    chan struct{}
	closeCount atomic.Int32
}

func (b *blockingSaveBackend) Save(context.Context, driver.Record, driver.SaveOptions) error {
	close(b.started)
	// Backends may finish after the producer cancels its bounded save.
	<-b.release
	return nil
}

func (b *blockingSaveBackend) Close(context.Context) error {
	b.closeCount.Add(1)
	return nil
}

func TestStoreCloseWaitsForInFlightSaveAfterCancellation(t *testing.T) {
	backend := &blockingSaveBackend{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	persistence, err := storage.NewStore[*Document](t.Context(), "blocking", backend, Collection, mapper{})
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{
		backends: []*storage.Store[*Document]{persistence},
		hub:      watch.NewHub[*Document](),
	}
	observed := timeutil.Now()
	document := &Document{Document: types.Document{
		TracerID:          "inflight-save",
		TracerRunType:     types.TracerRunTypeEvent,
		ObservedTimestamp: &observed,
	}}
	saveCtx, cancelSave := context.WithCancel(t.Context())
	defer cancelSave()
	saveDone := make(chan error, 1)
	saveFinished := make(chan struct{})
	go func() {
		defer close(saveFinished)
		saveDone <- store.SaveContext(saveCtx, document)
	}()
	t.Cleanup(func() {
		select {
		case <-backend.release:
		default:
			close(backend.release)
		}
		select {
		case <-saveFinished:
		case <-time.After(time.Second):
			t.Error("released save did not finish during cleanup")
		}
	})
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for in-flight save")
	}
	cancelSave()
	closeCtx, cancelClose := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelClose()
	if err := store.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want deadline", err)
	}
	if got := backend.closeCount.Load(); got != 0 {
		t.Fatalf("backend closed %d times while save was active", got)
	}
	if err := store.SaveContext(t.Context(), document); !errors.Is(err, errStoreClosing) {
		t.Fatalf("SaveContext() while closing error = %v", err)
	}
	close(backend.release)
	select {
	case err := <-saveDone:
		if err != nil {
			t.Fatalf("in-flight save error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight save did not finish")
	}
	if err := store.Close(t.Context()); err != nil {
		t.Fatalf("Close() after save error = %v", err)
	}
	if err := store.Close(t.Context()); err != nil {
		t.Fatalf("repeated Close() error = %v", err)
	}
	if got := backend.closeCount.Load(); got != 1 {
		t.Fatalf("backend close count = %d, want 1", got)
	}
	if err := store.Save(document); !errors.Is(err, errStoreClosing) {
		t.Fatalf("Save() after close error = %v", err)
	}
}
