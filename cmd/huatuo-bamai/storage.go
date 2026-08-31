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
	"huatuo-bamai/internal/nodeagent"
	"huatuo-bamai/internal/profiling/publication"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/internal/strutil"
	"huatuo-bamai/internal/tracing"
	profilingstore "huatuo-bamai/pkg/profiling/store"
	tracingstore "huatuo-bamai/pkg/tracing/store"
)

func setupStorage(d *Daemon) (func(context.Context) error, error) {
	if d.opts.DisableStorage {
		log.Infof("storage backends disabled by --disable-storage")
		d.tracingStore = tracingstore.New(nil)
		_ = tracing.ConfigureWriter(nil, nil)
		return nil, nil
	}

	tracingStore, profileStore, publicationStore, err := initStorage(d.opts.Region, config.Get())
	if err != nil {
		return nil, err
	}
	d.tracingStore = tracingStore
	d.profileStore = profileStore
	d.publications = publicationStore
	return func(ctx context.Context) error {
		var errs []error
		errs = append(errs, tracing.ConfigureWriter(nil, nil))
		if tracingStore != nil {
			errs = append(errs, wrapCloseError("tracing store", tracingStore.Close(ctx)))
		}
		if profileStore != nil {
			errs = append(errs, wrapCloseError("profiling store", profileStore.Close(ctx)))
		}
		if publicationStore != nil {
			errs = append(
				errs,
				wrapCloseError("profiling publication Store", publicationStore.Close(ctx)),
			)
		}
		return errors.Join(errs...)
	}, nil
}

func initStorage(
	storageRegion string,
	cfg *config.Config,
) (
	tracingStore *tracingstore.Store,
	profileStore *profilingstore.Store,
	publicationStore *publication.Store,
	returnedErr error,
) {
	tracingMetadataStores := make([]*storage.Store[*tracingstore.Document], 0, 2)
	defer func() {
		if returnedErr == nil {
			return
		}
		for _, store := range tracingMetadataStores {
			returnedErr = errors.Join(returnedErr, store.Close(context.Background()))
		}
		if profileStore != nil {
			returnedErr = errors.Join(returnedErr, profileStore.Close(context.Background()))
		}
		if publicationStore != nil {
			returnedErr = errors.Join(
				returnedErr,
				publicationStore.Close(context.Background()),
			)
		}
	}()

	if cfg.Storage.Elasticsearch.Enabled() {
		store, err := storage.NewFromConfig[*tracingstore.Document](context.Background(), &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: strutil.SplitCommaList(cfg.Storage.Elasticsearch.Address),
			ESUsername:  cfg.Storage.Elasticsearch.Username,
			ESPassword:  cfg.Storage.Elasticsearch.Password,
			ESIndex:     cfg.Storage.Elasticsearch.Index,
		}, tracingstore.Collection, tracingstore.Mapper{})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("new tracing document store (elasticsearch): %w", err)
		}
		tracingMetadataStores = append(tracingMetadataStores, store)
	}

	if cfg.Storage.LocalFile.Path != "" {
		localFileStore, err := storage.NewFromConfig[*tracingstore.Document](context.Background(), &driver.Config{
			Driver:                "localfile",
			LocalFilePath:         cfg.Storage.LocalFile.Path,
			LocalFileMaxRotation:  cfg.Storage.LocalFile.MaxRotatedFiles,
			LocalFileRotationSize: cfg.Storage.LocalFile.RotationSizeMiB,
		}, tracingstore.Collection, tracingstore.Mapper{})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("new tracing document store (localfile): %w", err)
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
		initializedProfileStore, err := profilingstore.New(
			context.Background(),
			profilingstore.Config{
				Addresses: storeConfig.ESAddresses,
				Username:  storeConfig.ESUsername,
				Password:  storeConfig.ESPassword,
				Index:     storeConfig.ESIndex,
			},
		)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("new profiling document store (elasticsearch): %w", err)
		}
		profileStore = initializedProfileStore
		publicationStore, err = publication.NewStore(context.Background(), storeConfig)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	tracingStore = tracingstore.New(tracingMetadataStores)
	if len(tracingMetadataStores) > 0 {
		if err := tracing.ConfigureWriter(
			tracingStore,
			nodeagent.NewDocumentBuilder(storageRegion, ""),
		); err != nil {
			return nil, nil, nil, err
		}
	}
	return tracingStore, profileStore, publicationStore, nil
}

func wrapCloseError(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close %s: %w", name, err)
}
