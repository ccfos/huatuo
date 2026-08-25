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
	"huatuo-bamai/internal/profiling/publication"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/internal/strutil"
	"huatuo-bamai/pkg/tracing"
)

func setupStorage(d *Daemon) (func(context.Context) error, error) {
	if d.opts.DisableStorage {
		log.Infof("storage backends disabled by --disable-storage")
		return nil, nil
	}

	publicationStore, err := initStorage(d.opts.Region, config.Get())
	if err != nil {
		return nil, err
	}
	d.publications = publicationStore
	return func(ctx context.Context) error {
		return errors.Join(
			wrapCloseError("tracing stores", tracing.CloseStores(ctx)),
			wrapCloseError("profiling publication Store", publicationStore.Close(ctx)),
		)
	}, nil
}

func initStorage(
	storageRegion string,
	cfg *config.Config,
) (publicationStore *publication.Store, returnedErr error) {
	tracingMetadataStores := make([]*storage.Store[*tracing.Document], 0, 2)
	profileMetadataStores := make([]*storage.Store[*tracing.Document], 0, 1)
	defer func() {
		if returnedErr == nil {
			return
		}
		allStores := make(
			[]*storage.Store[*tracing.Document],
			0,
			len(tracingMetadataStores)+len(profileMetadataStores),
		)
		allStores = append(allStores, tracingMetadataStores...)
		allStores = append(allStores, profileMetadataStores...)
		for _, store := range allStores {
			returnedErr = errors.Join(returnedErr, store.Close(context.Background()))
		}
		if publicationStore != nil {
			returnedErr = errors.Join(
				returnedErr,
				publicationStore.Close(context.Background()),
			)
		}
	}()

	if cfg.Storage.Elasticsearch.Enabled() {
		store, err := storage.NewFromConfig[*tracing.Document](context.Background(), &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: strutil.SplitCommaList(cfg.Storage.Elasticsearch.Address),
			ESUsername:  cfg.Storage.Elasticsearch.Username,
			ESPassword:  cfg.Storage.Elasticsearch.Password,
			ESIndex:     cfg.Storage.Elasticsearch.Index,
		}, tracing.DocumentCollection, tracing.DocumentStoreMapper{})
		if err != nil {
			return nil, fmt.Errorf("new tracing document store (elasticsearch): %w", err)
		}
		tracingMetadataStores = append(tracingMetadataStores, store)
	}

	if cfg.Storage.LocalFile.Path != "" {
		localFileStore, err := storage.NewFromConfig[*tracing.Document](context.Background(), &driver.Config{
			Driver:                "localfile",
			LocalFilePath:         cfg.Storage.LocalFile.Path,
			LocalFileMaxRotation:  cfg.Storage.LocalFile.MaxRotatedFiles,
			LocalFileRotationSize: cfg.Storage.LocalFile.RotationSizeMiB,
		}, tracing.DocumentCollection, tracing.DocumentStoreMapper{})
		if err != nil {
			return nil, fmt.Errorf("new tracing document store (localfile): %w", err)
		}
		tracingMetadataStores = append(tracingMetadataStores, localFileStore)
	}

	if cfg.Storage.Elasticsearch.Enabled() {
		storeConfig := &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: strutil.SplitCommaList(cfg.Storage.Elasticsearch.Address),
			ESUsername:  cfg.Storage.Elasticsearch.Username,
			ESPassword:  cfg.Storage.Elasticsearch.Password,
			ESIndex:     cfg.Storage.Elasticsearch.Index,
		}
		profileStore, err := storage.NewFromConfig[*tracing.Document](
			context.Background(),
			storeConfig,
			profiler.MetadataCollection,
			tracing.ProfileDocumentStoreMapper{},
		)
		if err != nil {
			return nil, fmt.Errorf("new profiling document store (elasticsearch): %w", err)
		}
		profileMetadataStores = append(profileMetadataStores, profileStore)
		publicationStore, err = publication.NewStore(context.Background(), storeConfig)
		if err != nil {
			return nil, err
		}
	}

	tracing.SetTracingStore(
		tracingMetadataStores,
		tracing.DocumentOptions{Region: storageRegion},
	)
	tracing.SetProfileStore(
		profileMetadataStores,
		tracing.DocumentOptions{Region: storageRegion},
	)

	return publicationStore, nil
}

func wrapCloseError(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close %s: %w", name, err)
}
