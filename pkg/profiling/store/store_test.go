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
	"reflect"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/types"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

func TestStoreSaveUsesAsynchronousWrite(t *testing.T) {
	store, backend := newTestStore(t)

	if err := store.Save(t.Context(), validDocument()); err != nil {
		t.Fatalf("Store.Save() error = %v", err)
	}
	if backend.asyncSaves != 1 {
		t.Fatalf("asynchronous saves = %d, want 1", backend.asyncSaves)
	}
	if backend.syncSaves != 0 {
		t.Fatalf("synchronous saves = %d, want 0", backend.syncSaves)
	}
}

func TestStoreSaveSyncUsesVisibilityBarrier(t *testing.T) {
	store, backend := newTestStore(t)

	if err := store.SaveSync(t.Context(), validDocument()); err != nil {
		t.Fatalf("Store.SaveSync() error = %v", err)
	}
	if backend.asyncSaves != 0 {
		t.Fatalf("asynchronous saves = %d, want 0", backend.asyncSaves)
	}
	if backend.syncSaves != 1 {
		t.Fatalf("synchronous saves = %d, want 1", backend.syncSaves)
	}
}

func TestStoreSaveRejectsIncompleteDocument(t *testing.T) {
	store, backend := newTestStore(t)

	if err := store.Save(t.Context(), &Document{}); err == nil {
		t.Fatal("Store.Save() error = nil, want validation error")
	}
	if backend.asyncSaves != 0 {
		t.Fatalf("asynchronous saves = %d, want 0", backend.asyncSaves)
	}
	if backend.syncSaves != 0 {
		t.Fatalf("synchronous saves = %d, want 0", backend.syncSaves)
	}
}

func TestMapperIDIsUniquePerWindow(t *testing.T) {
	document := validDocument()
	first := (mapper{}).ID(document)
	second := (mapper{}).ID(document)
	if first == "" || second == "" || first == second {
		t.Fatalf("mapper IDs = (%q, %q), want distinct non-empty IDs", first, second)
	}
	if document.TracerID != "profile-task-1" {
		t.Fatalf("document tracer ID = %q", document.TracerID)
	}
}

func TestMapperDecodeRejectsIncompleteDocument(t *testing.T) {
	startedTimestamp := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	encoded, err := json.Marshal(&Document{Document: types.Document{
		UploadedTimestamp: time.Date(2026, 8, 28, 2, 0, 1, 0, time.UTC),
		StartedTimestamp:  &startedTimestamp,
		TracerID:          "profile-task-1",
		TracerRunType:     types.TracerRunTypeProfiling,
	}})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	_, err = (mapper{}).Decode(driver.Record{Data: encoded})
	if err == nil {
		t.Fatal("mapper.Decode() error = nil")
	}
	if !strings.Contains(err.Error(), "profile data is required") {
		t.Fatalf("mapper.Decode() error = %q", err)
	}
}

func TestBuildProfileAggregationQueryPreservesTargetMatchers(t *testing.T) {
	query := buildAggregationQuery(&Filter{
		ContainerID:       "containerd://4df60fc5",
		ContainerHostname: "checkout-api-7b9f6d8c4f-k2x7m",
	})
	want := []driver.Filter{
		{
			Field: types.DocumentFieldContainerID + ".keyword",
			Op:    driver.OpEq,
			Value: "containerd://4df60fc5",
		},
		{
			Field: types.DocumentFieldContainerHostname + ".keyword",
			Op:    driver.OpEq,
			Value: "checkout-api-7b9f6d8c4f-k2x7m",
		},
	}

	if !reflect.DeepEqual(query.Filters, want) {
		t.Fatalf("query filters = %#v, want %#v", query.Filters, want)
	}
}

func TestBuildProfileAggregationQueryAddsTracerIDOnce(t *testing.T) {
	query := buildAggregationQuery(&Filter{TracerID: "task-20260722-8f6a"})
	want := []driver.Filter{
		{
			Field: types.DocumentFieldTracerID + ".keyword",
			Op:    driver.OpEq,
			Value: "task-20260722-8f6a",
		},
	}

	if !reflect.DeepEqual(query.Filters, want) {
		t.Fatalf("query filters = %#v, want %#v", query.Filters, want)
	}
}

func TestBuildProfileAggregationQueryFiltersByRegion(t *testing.T) {
	query := buildAggregationQuery(&Filter{
		Region:   "cn-beijing",
		Hostname: "node-1",
	})

	found := false
	for _, f := range query.Filters {
		if f.Field == types.DocumentFieldRegion+".keyword" && f.Value == "cn-beijing" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("query filters = %#v, want region filter present", query.Filters)
	}
}

type recordingBackend struct {
	asyncSaves int
	syncSaves  int
}

func (*recordingBackend) Init(context.Context, string, []driver.Index) error { return nil }

func (b *recordingBackend) Save(
	_ context.Context,
	_ driver.Record,
	options driver.SaveOptions,
) error {
	if options.WaitForVisibility {
		b.syncSaves++
	} else {
		b.asyncSaves++
	}
	return nil
}

func (*recordingBackend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrNotFound
}

func (*recordingBackend) Delete(context.Context, string) error { return nil }

func (*recordingBackend) DeleteByQuery(context.Context, driver.DeleteQuery) (int64, error) {
	return 0, nil
}

func (*recordingBackend) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, nil
}

func (*recordingBackend) Count(context.Context, driver.Query) (int64, error) { return 0, nil }

func (*recordingBackend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, nil
}

func (*recordingBackend) Close(context.Context) error { return nil }

func newTestStore(t *testing.T) (*Store, *recordingBackend) {
	t.Helper()
	backend := &recordingBackend{}
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
	return &Store{store: persistence}, backend
}

func validDocument() *Document {
	startedTimestamp := time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC)
	return &Document{
		Document: types.Document{
			StartedTimestamp: &startedTimestamp,
			TracerID:         "profile-task-1",
			TracerRunType:    types.TracerRunTypeProfiling,
		},
		ProfileData: &ProfileData{
			ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			Profile:     &profilev1.Profile{},
		},
	}
}
