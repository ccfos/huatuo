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

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/pkg/tracing"
)

func setupStorage(d *Daemon) (func(context.Context) error, error) {
	if d.opts.DisableStorage {
		log.Infof("storage backends disabled by --disable-storage")
		return nil, nil
	}

	return nil, initStorage(d.opts.Region, config.Get())
}

func initStorage(storageRegion string, cfg *config.BamaiConfig) error {
	var esStore *storage.Store[*tracing.Document]

	tracingMetadataStores := make([]*storage.Store[*tracing.Document], 0, 2)
	if cfg.Storage.ES.Address != "" &&
		cfg.Storage.ES.Username != "" &&
		cfg.Storage.ES.Password != "" {
		store, err := storage.NewFromConfig[*tracing.Document](context.Background(), &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: splitStorageAddresses(cfg.Storage.ES.Address),
			ESUsername:  cfg.Storage.ES.Username,
			ESPassword:  cfg.Storage.ES.Password,
			ESIndex:     cfg.Storage.ES.Index,
		}, tracing.DocumentCollection, tracing.DocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("new tracing document store (elasticsearch): %w", err)
		}
		esStore = store
		tracingMetadataStores = append(tracingMetadataStores, esStore)
	}

	if cfg.Storage.LocalFile.Path != "" {
		localFileStore, err := storage.NewFromConfig[*tracing.Document](context.Background(), &driver.Config{
			Driver:                "localfile",
			LocalFilePath:         cfg.Storage.LocalFile.Path,
			LocalFileMaxRotation:  cfg.Storage.LocalFile.MaxRotation,
			LocalFileRotationSize: cfg.Storage.LocalFile.RotationSize,
		}, tracing.DocumentCollection, tracing.DocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("new tracing document store (localfile): %w", err)
		}
		tracingMetadataStores = append(tracingMetadataStores, localFileStore)
	}

	if len(tracingMetadataStores) > 0 {
		tracing.SetTracingStore(
			tracingMetadataStores,
			tracing.DocumentOptions{
				Region: storageRegion,
			},
		)
	}
	if esStore != nil {
		tracing.SetTaskStore([]*storage.Store[*tracing.Document]{esStore}, tracing.DocumentOptions{Region: storageRegion})
	}

	if cfg.Storage.ES.Address != "" &&
		cfg.Storage.ES.Username != "" &&
		cfg.Storage.ES.Password != "" {
		profileStore, err := storage.NewFromConfig[*tracing.Document](context.Background(), &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: splitStorageAddresses(cfg.Storage.ES.Address),
			ESUsername:  cfg.Storage.ES.Username,
			ESPassword:  cfg.Storage.ES.Password,
			ESIndex:     profiler.MetadataCollection,
		}, profiler.MetadataCollection, profiler.ProfilingDocumentMapper{})
		if err != nil {
			return fmt.Errorf("new profiling document store (elasticsearch): %w", err)
		}
		tracing.SetProfileStore(
			[]*storage.Store[*tracing.Document]{profileStore},
			tracing.DocumentOptions{Region: storageRegion},
		)
	}

	return nil
}

func splitStorageAddresses(raw string) []string {
	parts := strings.Split(raw, ",")
	addresses := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		addresses = append(addresses, trimmed)
	}
	return addresses
}
