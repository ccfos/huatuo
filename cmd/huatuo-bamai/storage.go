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
	"huatuo-bamai/internal/document"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiling/publication"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/internal/strutil"
	"huatuo-bamai/internal/tracing"
	profilingstore "huatuo-bamai/pkg/profiling/store"
	tracingstore "huatuo-bamai/pkg/tracing/store"
)

func setupStorage(d *Daemon) (func(context.Context) error, error) {
	var (
		tracingStore     *tracingstore.Store
		profileStore     *profilingstore.Store
		publicationStore *publication.Store
		err              error
	)

	if d.opts.DisableStorage {
		log.Infof("storage backends disabled by --disable-storage")
		tracingStore, err = tracingstore.NewFromConfig(
			context.Background(),
			tracingstore.Config{},
		)
	} else {
		tracingStore, profileStore, publicationStore, err = initStorage(config.Get())
	}

	if err != nil {
		return nil, err
	}
	if err := tracing.EnableDocumentWriter(
		tracingStore,
		document.New(d.opts.Region),
	); err != nil {
		return nil, errors.Join(
			err,
			closeStores(context.Background(), tracingStore, profileStore, publicationStore),
		)
	}
	d.tracingStore = tracingStore
	d.profileStore = profileStore
	d.publications = publicationStore
	return func(ctx context.Context) error {
		tracing.DisableDocumentWriter()
		return closeStores(ctx, tracingStore, profileStore, publicationStore)
	}, nil
}

func initStorage(
	cfg *config.Config,
) (
	tracingStore *tracingstore.Store,
	profileStore *profilingstore.Store,
	publicationStore *publication.Store,
	returnedErr error,
) {
	defer func() {
		if returnedErr != nil {
			returnedErr = errors.Join(
				returnedErr,
				closeStores(
					context.Background(),
					tracingStore,
					profileStore,
					publicationStore,
				),
			)
		}
	}()

	tracingConfig := tracingstore.Config{}
	if cfg.Storage.Elasticsearch.Enabled() {
		tracingConfig.Elasticsearch = &tracingstore.ElasticsearchConfig{
			Addresses: strutil.SplitCommaList(cfg.Storage.Elasticsearch.Address),
			Username:  cfg.Storage.Elasticsearch.Username,
			Password:  cfg.Storage.Elasticsearch.Password,
			Index:     cfg.Storage.Elasticsearch.Index,
		}
	}
	if cfg.Storage.LocalFile.Path != "" {
		tracingConfig.LocalFile = &tracingstore.LocalFileConfig{
			Path:            cfg.Storage.LocalFile.Path,
			RotationSizeMiB: cfg.Storage.LocalFile.RotationSizeMiB,
			MaxRotatedFiles: cfg.Storage.LocalFile.MaxRotatedFiles,
		}
	}
	initializedTracingStore, err := tracingstore.NewFromConfig(
		context.Background(),
		tracingConfig,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	tracingStore = initializedTracingStore

	if cfg.Storage.Elasticsearch.Enabled() {
		storeConfig := &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: strutil.SplitCommaList(cfg.Storage.Elasticsearch.Address),
			ESUsername:  cfg.Storage.Elasticsearch.Username,
			ESPassword:  cfg.Storage.Elasticsearch.Password,
			ESIndex:     cfg.Storage.Elasticsearch.Index,
		}
		initializedProfileStore, err := profilingstore.NewFromConfig(
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

	return tracingStore, profileStore, publicationStore, nil
}

func closeStores(
	ctx context.Context,
	tracingStore *tracingstore.Store,
	profileStore *profilingstore.Store,
	publicationStore *publication.Store,
) error {
	var errs []error
	if tracingStore != nil {
		errs = append(errs, wrapCloseError("tracing store", tracingStore.Close(ctx)))
	}
	if profileStore != nil {
		errs = append(errs, wrapCloseError("profiling store", profileStore.Close(ctx)))
	}
	if publicationStore != nil {
		errs = append(
			errs,
			wrapCloseError("profiling publication store", publicationStore.Close(ctx)),
		)
	}
	return errors.Join(errs...)
}

func wrapCloseError(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close %s: %w", name, err)
}
