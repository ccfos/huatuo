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

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/runmode"
	"github.com/ccfos/huatuo/internal/strutil"
	"github.com/ccfos/huatuo/internal/tracing"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

func setupStorage(d *Daemon) (func(context.Context) error, error) {
	var (
		tracingStore *tracingstore.Store
		err          error
	)

	if d.opts.DisableStorage {
		log.Infof("storage backends disabled by --disable-storage")
		tracingStore, err = tracingstore.NewFromConfig(
			context.Background(),
			tracingstore.Config{},
		)
	} else {
		tracingStore, err = initStorage(config.Get())
	}

	if err != nil {
		return nil, err
	}
	if err := tracing.EnableDocumentWriter(
		tracingStore,
		newDocumentBuilder(d.opts),
	); err != nil {
		return nil, errors.Join(
			err,
			tracingStore.Close(context.Background()),
		)
	}
	d.tracingStore = tracingStore
	return func(ctx context.Context) error {
		tracing.DisableDocumentWriter()
		return tracingStore.Close(ctx)
	}, nil
}

func initStorage(
	cfg *config.Config,
) (
	tracingStore *tracingstore.Store,
	returnedErr error,
) {
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
		return nil, err
	}
	tracingStore = initializedTracingStore

	return tracingStore, nil
}

func newDocumentBuilder(opts *Options) *document.Builder {
	if opts.Mode == runmode.Sandbox {
		return document.NewWithSource(opts.Region, opts.GuestIdentity.NodeName, types.DocumentSourceKataGuest)
	}
	return document.New(opts.Region)
}
