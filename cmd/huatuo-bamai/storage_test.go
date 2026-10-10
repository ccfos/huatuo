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

package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/storage/elasticsearch"
)

// failingBackend stands in for the Elasticsearch backend: it records the
// collection it is initialized with and fails the one the test selects.
type failingBackend struct {
	factory    *backendFactory
	collection string
	closed     bool
}

func (b *failingBackend) Init(_ context.Context, collection string, _ []driver.Index) error {
	b.collection = collection
	if collection == b.factory.failOn {
		return fmt.Errorf("injected failure for collection %q", collection)
	}
	return nil
}

func (b *failingBackend) Save(context.Context, driver.Record, driver.SaveOptions) error {
	return nil
}

func (b *failingBackend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrNotFound
}

func (b *failingBackend) Delete(context.Context, string) error { return nil }

func (b *failingBackend) DeleteByQuery(context.Context, driver.DeleteQuery) (int64, error) {
	return 0, nil
}

func (b *failingBackend) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, nil
}

func (b *failingBackend) Count(context.Context, driver.Query) (int64, error) { return 0, nil }

func (b *failingBackend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, nil
}

func (b *failingBackend) Close(context.Context) error {
	b.closed = true
	return nil
}

type backendFactory struct {
	failOn   string
	mu       sync.Mutex
	backends []*failingBackend
}

func (f *backendFactory) newBackend() driver.Backend {
	f.mu.Lock()
	defer f.mu.Unlock()
	backend := &failingBackend{factory: f}
	f.backends = append(f.backends, backend)
	return backend
}

func (f *backendFactory) created() []*failingBackend {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*failingBackend(nil), f.backends...)
}

// useFailingElasticsearchBackend replaces the elasticsearch driver for one test
// and restores it afterwards.
func useFailingElasticsearchBackend(t *testing.T, factory *backendFactory) {
	t.Helper()
	driver.RegisterBackend("elasticsearch", func(*driver.Config) (driver.Backend, error) {
		return factory.newBackend(), nil
	})
	t.Cleanup(func() {
		driver.RegisterBackend("elasticsearch", func(cfg *driver.Config) (driver.Backend, error) {
			return elasticsearch.NewBackend(&elasticsearch.Config{
				Addresses: cfg.ESAddresses,
				Username:  cfg.ESUsername,
				Password:  cfg.ESPassword,
				Index:     cfg.ESIndex,
			})
		})
	})
}

func elasticsearchConfigForTest() *config.Config {
	cfg := &config.Config{}
	cfg.Storage.Elasticsearch.Address = "127.0.0.1:9200"
	cfg.Storage.Elasticsearch.Username = "elastic"
	cfg.Storage.Elasticsearch.Password = "secret"
	cfg.Storage.Elasticsearch.Index = "huatuo-test"
	return cfg
}

// TestInitStorageClosesTheTracingStoreWhenProfilingFails covers the cleanup:
// the tracing store is opened first, and when a later store fails the deferred
// cleanup has to close it. Reading the named results of initStorage there would
// see nil - the error returns reset them before deferred functions run - and
// close nothing.
func TestInitStorageClosesTheTracingStoreWhenProfilingFails(t *testing.T) {
	factory := &backendFactory{failOn: "profiling_metadata"}
	useFailingElasticsearchBackend(t, factory)

	tracingStore, profileStore, publicationStore, err := initStorage(elasticsearchConfigForTest())
	if err == nil {
		t.Fatal("initStorage() error = nil, want the profiling store failure")
	}
	if !strings.Contains(err.Error(), "profiling document store") {
		t.Fatalf("initStorage() error = %v, want the profiling store error", err)
	}
	if tracingStore != nil || profileStore != nil || publicationStore != nil {
		t.Fatalf("initStorage() returned stores together with an error: %v, %v, %v",
			tracingStore, profileStore, publicationStore)
	}

	backends := factory.created()
	if len(backends) != 2 {
		t.Fatalf("backends created = %d, want the tracing and profiling ones", len(backends))
	}
	if backends[0].collection != "tracing_documents" {
		t.Fatalf("first backend initialized for %q, want the tracing collection", backends[0].collection)
	}
	if !backends[0].closed {
		t.Fatal("the tracing store opened before the failure was never closed")
	}
}

// TestInitStorageKeepsTheStoresOnSuccess is the other half: nothing is closed
// while the initialization succeeds, and the stores can be closed later.
func TestInitStorageKeepsTheStoresOnSuccess(t *testing.T) {
	factory := &backendFactory{}
	useFailingElasticsearchBackend(t, factory)

	tracingStore, profileStore, publicationStore, err := initStorage(elasticsearchConfigForTest())
	if err != nil {
		t.Fatalf("initStorage() error = %v", err)
	}
	if tracingStore == nil || profileStore == nil || publicationStore == nil {
		t.Fatal("initStorage() returned nil stores on success")
	}

	for _, backend := range factory.created() {
		if backend.closed {
			t.Fatalf("backend for %q was closed during a successful initialization", backend.collection)
		}
	}

	if err := closeStores(context.Background(), tracingStore, profileStore, publicationStore); err != nil {
		t.Fatalf("closeStores() error = %v", err)
	}
	for _, backend := range factory.created() {
		if !backend.closed {
			t.Fatalf("backend for %q was not closed by closeStores", backend.collection)
		}
	}
}
