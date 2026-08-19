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
	"errors"
	"strings"
	"testing"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	internalconfig "huatuo-bamai/internal/config"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/tracing"
)

type storageTestBackend struct {
	closeCalls int
	closeCtx   context.Context
	closeErr   error
	closeLog   *[]string
	saveCalls  int
}

func (*storageTestBackend) Init(context.Context, string, []driver.Index) error {
	return nil
}

func (b *storageTestBackend) Save(context.Context, driver.Record) error {
	b.saveCalls++
	return nil
}

func (*storageTestBackend) Get(context.Context, string) (driver.Record, error) {
	return driver.Record{}, driver.ErrNotFound
}

func (*storageTestBackend) Delete(context.Context, string) error {
	return nil
}

func (*storageTestBackend) Query(context.Context, driver.Query) ([]driver.Record, error) {
	return nil, nil
}

func (*storageTestBackend) Count(context.Context, driver.Query) (int64, error) {
	return 0, nil
}

func (*storageTestBackend) Values(context.Context, string, driver.Query, int) ([]string, error) {
	return nil, nil
}

func (b *storageTestBackend) Close(ctx context.Context) error {
	b.closeCalls++
	b.closeCtx = ctx
	if b.closeLog != nil {
		*b.closeLog = append(*b.closeLog, "storage")
	}
	return b.closeErr
}

type storageTestManagerCloser struct {
	closeLog *[]string
}

func (m *storageTestManagerCloser) Close(context.Context) error {
	*m.closeLog = append(*m.closeLog, "tracing")
	return nil
}

func resetTracingStores() {
	tracing.SetTracingStore(nil, tracing.DocumentOptions{})
	tracing.SetTaskStore(nil, tracing.DocumentOptions{})
	tracing.SetProfileStore(nil, tracing.DocumentOptions{})
}

func TestSetupStorageRegistersCleanup(t *testing.T) {
	resetTracingStores()
	t.Cleanup(resetTracingStores)

	cleanup, err := setupStorage(&Daemon{opts: &Options{DisableStorage: true}})
	if err != nil {
		t.Fatalf("setupStorage(disabled) error = %v", err)
	}
	if cleanup != nil {
		t.Fatal("setupStorage(disabled) cleanup is non-nil")
	}

	cleanup, err = setupStorage(&Daemon{opts: &Options{}})
	if err != nil {
		t.Fatalf("setupStorage() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("setupStorage() cleanup is nil")
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("storage cleanup error = %v", err)
	}
}

func TestStorageCleanupHasSingleOwnerAndRunsBeforeBPF(t *testing.T) {
	resetTracingStores()
	t.Cleanup(resetTracingStores)

	var closeLog []string
	backend := &storageTestBackend{closeLog: &closeLog}
	store, err := storage.NewStore[*tracing.Document](
		context.Background(),
		"test",
		backend,
		tracing.DocumentCollection,
		tracing.DocumentStoreMapper{},
	)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	tracing.SetTracingStore(
		[]*storage.Store[*tracing.Document]{store},
		tracing.DocumentOptions{},
	)

	tracingCleanup := closeTracingManager(&storageTestManagerCloser{closeLog: &closeLog})
	if err := tracingCleanup(context.Background()); err != nil {
		t.Fatalf("tracing cleanup error = %v", err)
	}
	if err := tracing.CloseStores(context.Background()); err != nil {
		t.Fatalf("storage cleanup error = %v", err)
	}
	if backend.closeCalls != 1 {
		t.Fatalf("backend close calls = %d, want 1", backend.closeCalls)
	}
	if got := strings.Join(closeLog, ","); got != "tracing,storage" {
		t.Fatalf("cleanup order = %q, want tracing,storage", got)
	}

	positions := make(map[string]int)
	for i, step := range daemonSetupSteps() {
		positions[step.name] = i
	}
	if !(positions["bpf"] < positions["storage"] &&
		positions["storage"] < positions["tracing"]) {
		t.Fatalf(
			"setup order bpf=%d storage=%d tracing=%d does not produce tracing, storage, bpf cleanup",
			positions["bpf"],
			positions["storage"],
			positions["tracing"],
		)
	}
}

func TestInitStoragePublishesOnlyAfterCompleteConstruction(t *testing.T) {
	resetTracingStores()
	t.Cleanup(resetTracingStores)

	cfg := storageTestConfig()
	var backends []*storageTestBackend
	var collections []string
	shutdownErr := errors.New("profile store close failed")
	factory := func(
		ctx context.Context,
		storeConfig *driver.Config,
		collection string,
		mapper driver.Mapper[*tracing.Document],
	) (*storage.Store[*tracing.Document], error) {
		backend := &storageTestBackend{}
		if collection == profiler.MetadataCollection {
			backend.closeErr = shutdownErr
		}
		backends = append(backends, backend)
		collections = append(collections, collection)
		return storage.NewStore(ctx, storeConfig.Driver, backend, collection, mapper)
	}

	if err := initStorageWithFactory("test-region", cfg, factory); err != nil {
		t.Fatalf("initStorageWithFactory() error = %v", err)
	}
	wantCollections := []string{
		tracing.DocumentCollection,
		tracing.DocumentCollection,
		profiler.MetadataCollection,
	}
	if len(collections) != len(wantCollections) {
		t.Fatalf("constructed collections = %v, want %v", collections, wantCollections)
	}
	for i := range wantCollections {
		if collections[i] != wantCollections[i] {
			t.Fatalf("collection %d = %q, want %q", i, collections[i], wantCollections[i])
		}
	}

	type contextKey string
	shutdownCtx := context.WithValue(context.Background(), contextKey("shutdown"), true)
	if err := tracing.CloseStores(shutdownCtx); !errors.Is(err, shutdownErr) {
		t.Fatalf("CloseStores() error = %v, want profile close error", err)
	}
	if len(backends) != 3 {
		t.Fatalf("constructed backends = %d, want 3", len(backends))
	}
	for i, backend := range backends {
		if backend.closeCalls != 1 {
			t.Fatalf("backend %d close calls = %d, want 1", i, backend.closeCalls)
		}
		if backend.closeCtx != shutdownCtx {
			t.Fatalf("backend %d did not receive shutdown context", i)
		}
	}
}

func TestInitStorageClosesEarlierStoresOnFailure(t *testing.T) {
	resetTracingStores()
	t.Cleanup(resetTracingStores)

	cfg := storageTestConfig()
	constructorErr := errors.New("profile store construction failed")
	closeErr := errors.New("tracing store close failed")
	first := &storageTestBackend{closeErr: closeErr}
	second := &storageTestBackend{}
	backends := []*storageTestBackend{first, second}
	call := 0
	factory := func(
		ctx context.Context,
		storeConfig *driver.Config,
		collection string,
		mapper driver.Mapper[*tracing.Document],
	) (*storage.Store[*tracing.Document], error) {
		if call == len(backends) {
			return nil, constructorErr
		}
		backend := backends[call]
		call++
		return storage.NewStore(ctx, storeConfig.Driver, backend, collection, mapper)
	}

	err := initStorageWithFactory("test-region", cfg, factory)
	if !errors.Is(err, constructorErr) {
		t.Fatalf("init error = %v, want constructor error", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("init error = %v, want cleanup error", err)
	}
	for i, backend := range backends {
		if backend.closeCalls != 1 {
			t.Fatalf("backend %d close calls = %d, want 1", i, backend.closeCalls)
		}
	}

	request := &tracing.WriteRequest{}
	if err := tracing.Save(request); err != nil {
		t.Fatalf("Save() after failed setup error = %v", err)
	}
	if err := tracing.SaveTaskOutputText(request); err != nil {
		t.Fatalf("SaveTaskOutputText() after failed setup error = %v", err)
	}
	if err := tracing.SaveProfile(request); err != nil {
		t.Fatalf("SaveProfile() after failed setup error = %v", err)
	}
	for i, backend := range backends {
		if backend.saveCalls != 0 {
			t.Fatalf("backend %d save calls = %d after failed setup", i, backend.saveCalls)
		}
	}
}

func storageTestConfig() *config.Config {
	return &config.Config{
		Storage: config.StorageConfig{
			Elasticsearch: internalconfig.ElasticsearchConfig{
				Address:  "http://127.0.0.1:9200",
				Username: "user",
				Password: "password",
				Index:    "huatuo-test",
			},
			LocalFile: config.LocalFileConfig{
				Path:            "/tmp/huatuo-test",
				RotationSizeMiB: 1,
				MaxRotatedFiles: 1,
			},
		},
	}
}
