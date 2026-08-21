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

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
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

	return nil, initStorage(d.opts.Region, config.Get())
}

func initStorage(storageRegion string, cfg *config.Config) error {
	// Every queryable backend serves all three document roles; localfile is
	// append-only and therefore receives tracing metadata only. Configuring
	// more than one backend fans writes out to all of them.
	backendConfigs := make([]*driver.Config, 0, 2)
	if cfg.Storage.Elasticsearch.Enabled() {
		backendConfigs = append(backendConfigs, &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: strutil.SplitCommaList(cfg.Storage.Elasticsearch.Address),
			ESUsername:  cfg.Storage.Elasticsearch.Username,
			ESPassword:  cfg.Storage.Elasticsearch.Password,
			ESIndex:     cfg.Storage.Elasticsearch.Index,
		})
	}
	if cfg.Storage.Doris.Enabled() {
		backendConfigs = append(backendConfigs, cfg.Storage.Doris.DriverConfig())
	}

	tracingMetadataStores := make([]*storage.Store[*tracing.Document], 0, len(backendConfigs)+1)
	taskStores := make([]*storage.Store[*tracing.Document], 0, len(backendConfigs))
	profileStores := make([]*storage.Store[*tracing.Document], 0, len(backendConfigs))

	for _, backendConfig := range backendConfigs {
		documentStore, err := storage.NewFromConfig[*tracing.Document](context.Background(), backendConfig,
			tracing.DocumentCollection, tracing.DocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("new tracing document store (%s): %w", backendConfig.Driver, err)
		}
		tracingMetadataStores = append(tracingMetadataStores, documentStore)
		taskStores = append(taskStores, documentStore)

		profileStore, err := storage.NewFromConfig[*tracing.Document](context.Background(), backendConfig,
			profiler.MetadataCollection, tracing.ProfileDocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("new profiling document store (%s): %w", backendConfig.Driver, err)
		}
		profileStores = append(profileStores, profileStore)
	}

	if cfg.Storage.LocalFile.Path != "" {
		localFileStore, err := storage.NewFromConfig[*tracing.Document](context.Background(), &driver.Config{
			Driver:                "localfile",
			LocalFilePath:         cfg.Storage.LocalFile.Path,
			LocalFileMaxRotation:  cfg.Storage.LocalFile.MaxRotatedFiles,
			LocalFileRotationSize: cfg.Storage.LocalFile.RotationSizeMiB,
		}, tracing.DocumentCollection, tracing.DocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("new tracing document store (localfile): %w", err)
		}
		tracingMetadataStores = append(tracingMetadataStores, localFileStore)
	}

	options := tracing.DocumentOptions{Region: storageRegion}
	if len(tracingMetadataStores) > 0 {
		tracing.SetTracingStore(tracingMetadataStores, options)
	}
	if len(taskStores) > 0 {
		tracing.SetTaskStore(taskStores, options)
	}
	if len(profileStores) > 0 {
		tracing.SetProfileStore(profileStores, options)
	}

	return nil
}
