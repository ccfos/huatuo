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
	"fmt"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/internal/strutil"
	"huatuo-bamai/pkg/tracing"
)

type documentStoreFactory func(
	context.Context,
	*driver.Config,
	string,
	driver.Mapper[*tracing.Document],
) (*storage.Store[*tracing.Document], error)

func setupStorage(d *Daemon) (func(context.Context) error, error) {
	if d.opts.DisableStorage {
		log.Infof("storage backends disabled by --disable-storage")
		return nil, nil
	}

	if err := initStorage(d.opts.Region, config.Get()); err != nil {
		return nil, err
	}
	return tracing.CloseStores, nil
}

func initStorage(storageRegion string, cfg *config.Config) error {
	return initStorageWithFactory(
		storageRegion,
		cfg,
		storage.NewFromConfig[*tracing.Document],
	)
}

func initStorageWithFactory(
	storageRegion string,
	cfg *config.Config,
	newStore documentStoreFactory,
) (retErr error) {
	var esStore *storage.Store[*tracing.Document]
	var profileStore *storage.Store[*tracing.Document]

	tracingMetadataStores := make([]*storage.Store[*tracing.Document], 0, 2)
	initializedStores := make([]*storage.Store[*tracing.Document], 0, 3)
	defer func() {
		if retErr != nil {
			retErr = errors.Join(
				retErr,
				closeInitializedStores(context.Background(), initializedStores),
			)
		}
	}()

	if cfg.Storage.Elasticsearch.Enabled() {
		store, err := newStore(context.Background(), &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: strutil.SplitCommaList(cfg.Storage.Elasticsearch.Address),
			ESUsername:  cfg.Storage.Elasticsearch.Username,
			ESPassword:  cfg.Storage.Elasticsearch.Password,
			ESIndex:     cfg.Storage.Elasticsearch.Index,
		}, tracing.DocumentCollection, tracing.DocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("new tracing document store (elasticsearch): %w", err)
		}
		esStore = store
		tracingMetadataStores = append(tracingMetadataStores, esStore)
		initializedStores = append(initializedStores, esStore)
	}

	if cfg.Storage.LocalFile.Path != "" {
		localFileStore, err := newStore(context.Background(), &driver.Config{
			Driver:                "localfile",
			LocalFilePath:         cfg.Storage.LocalFile.Path,
			LocalFileMaxRotation:  cfg.Storage.LocalFile.MaxRotatedFiles,
			LocalFileRotationSize: cfg.Storage.LocalFile.RotationSizeMiB,
		}, tracing.DocumentCollection, tracing.DocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("new tracing document store (localfile): %w", err)
		}
		tracingMetadataStores = append(tracingMetadataStores, localFileStore)
		initializedStores = append(initializedStores, localFileStore)
	}

	if cfg.Storage.Elasticsearch.Enabled() {
		store, err := newStore(context.Background(), &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: strutil.SplitCommaList(cfg.Storage.Elasticsearch.Address),
			ESUsername:  cfg.Storage.Elasticsearch.Username,
			ESPassword:  cfg.Storage.Elasticsearch.Password,
			ESIndex:     cfg.Storage.Elasticsearch.Index,
		}, profiler.MetadataCollection, tracing.ProfileDocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("new profiling document store (elasticsearch): %w", err)
		}
		profileStore = store
		initializedStores = append(initializedStores, profileStore)
	}

	options := tracing.DocumentOptions{Region: storageRegion}
	if len(tracingMetadataStores) > 0 {
		tracing.SetTracingStore(tracingMetadataStores, options)
	}
	if esStore != nil {
		tracing.SetTaskStore([]*storage.Store[*tracing.Document]{esStore}, options)
	}
	if profileStore != nil {
		tracing.SetProfileStore([]*storage.Store[*tracing.Document]{profileStore}, options)
	}

	return nil
}

func closeInitializedStores(
	ctx context.Context,
	stores []*storage.Store[*tracing.Document],
) error {
	seen := make(map[*storage.Store[*tracing.Document]]struct{}, len(stores))
	var errs []error
	for _, store := range stores {
		if store == nil {
			continue
		}
		if _, ok := seen[store]; ok {
			continue
		}
		seen[store] = struct{}{}
		if err := store.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close initialized store %q: %w", store.Name, err))
		}
	}
	return errors.Join(errs...)
}
