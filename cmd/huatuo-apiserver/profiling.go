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

	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/internal/strutil"
)

func setupProfileQueryService(ctx context.Context, d *Daemon) (func(context.Context) error, error) {
	storageConfig := profileStorageConfig(d.opts.Config)
	if storageConfig == nil {
		log.Info("profile storage disabled")
		return nil, nil
	}

	profileQueryService, err := service.NewService(ctx, storageConfig)
	if err != nil {
		return nil, fmt.Errorf("initialize profile query service: %w", err)
	}
	d.profileQueryService = profileQueryService

	return profileQueryService.Close, nil
}

// profileStorageConfig selects the backend profiles are read from, returning
// nil when none is configured. Doris takes precedence so an operator migrating
// off Elasticsearch can keep both sections in place and switch the read side by
// adding the Doris section alone.
func profileStorageConfig(cfg *config.Config) *driver.Config {
	if cfg.Doris.Enabled() {
		return cfg.Doris.DriverConfig()
	}
	if cfg.Elasticsearch.Enabled() {
		return &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: strutil.SplitCommaList(cfg.Elasticsearch.Address),
			ESUsername:  cfg.Elasticsearch.Username,
			ESPassword:  cfg.Elasticsearch.Password,
			ESIndex:     cfg.Elasticsearch.Index,
		}
	}
	return nil
}
