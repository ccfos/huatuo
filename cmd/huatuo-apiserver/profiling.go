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

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/service"
)

func setupProfileQueryService(ctx context.Context, d *Daemon) (func(context.Context) error, error) {
	if !d.opts.Config.Elasticsearch.Enabled() {
		log.Info("profile storage disabled")
		return nil, nil
	}

	esConfig := &service.ElasticSearchConfig{
		Address:  d.opts.Config.Elasticsearch.Address,
		Username: d.opts.Config.Elasticsearch.Username,
		Password: d.opts.Config.Elasticsearch.Password,
		Index:    d.opts.Config.Elasticsearch.Index,

		InsecureSkipVerify: d.opts.Config.Elasticsearch.InsecureSkipVerify,
		CABundle:           d.opts.Config.Elasticsearch.CABundle,
	}
	profileQueryService, err := service.NewService(ctx, esConfig)
	if err != nil {
		return nil, fmt.Errorf("initialize profile query service: %w", err)
	}
	d.profileQueryService = profileQueryService

	return profileQueryService.Close, nil
}
