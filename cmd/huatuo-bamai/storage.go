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
	"time"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/runmode"
	"github.com/ccfos/huatuo/internal/storage/vsock"
	"github.com/ccfos/huatuo/internal/strutil"
	"github.com/ccfos/huatuo/internal/tracing"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

func setupStorage(d *Daemon) (func(context.Context) error, error) {
	var (
		tracingStore *tracingstore.Store
	profileStore *profilingstore.Store
		err          error
	)

	if d.opts.DisableStorage {
		log.Infof("storage backends disabled by --disable-storage")
		tracingStore, err = tracingstore.NewFromConfig(
			context.Background(),
			tracingstore.Config{},
		)
	} else {
		tracingStore, profileStore, err = initStorage(config.Get())
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
			closeStores(context.Background(), tracingStore, profileStore),
		)
	}
	d.tracingStore = tracingStore
	d.profileStore = profileStore
	return func(ctx context.Context) error {
		tracing.DisableDocumentWriter()
		return closeStores(ctx, tracingStore, profileStore)
	}, nil
}

func initStorage(
	cfg *config.Config,
) (
	tracingStore *tracingstore.Store
	profileStore *profilingstore.Store,
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
				),
			)
		}
	}()

	if cfg.GuestTransport.Enabled {
		g := cfg.GuestTransport
		options := &vsock.Options{Port: g.Port, Version: AppVersion, MaxMessageSize: g.MaxMessageBytes, MaxPendingBytes: g.MaxPendingBytes, IOTimeout: time.Duration(g.IOTimeoutSeconds) * time.Second}
		tracingStore, err := tracingstore.NewFromConfig(context.Background(), tracingstore.Config{VSOCK: options})
		if err != nil {
			return nil, nil, err
		}
		profileStore, err := profilingstore.NewFromConfig(context.Background(), profilingstore.Config{VSOCK: options})
		if err != nil {
			_ = closeStores(context.Background(), tracingStore, profileStore)
			return nil, nil, err
		}
		return tracingStore, profileStore, nil
	}
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
		return nil, nil, err
	}
	tracingStore = initializedTracingStore

	if cfg.Storage.Elasticsearch.Enabled() {
        es := cfg.Storage.Elasticsearch
        profileStore, returnedErr = profilingstore.NewFromConfig(context.Background(), profilingstore.Config{
            Addresses: strutil.SplitCommaList(es.Address), Username: es.Username,
            Password: es.Password, Index: es.Index,
        })
    }
    return tracingStore, profileStore, returnedErr
}

func closeStores(ctx context.Context, tracingStore *tracingstore.Store, profileStore *profilingstore.Store) error {
    var errs []error
    if tracingStore != nil { errs = append(errs, tracingStore.Close(ctx)) }
    if profileStore != nil { errs = append(errs, profileStore.Close(ctx)) }
    return errors.Join(errs...)
}

func newDocumentBuilder(opts *Options) *document.Builder {
	if opts.Mode == runmode.Sandbox {
		return document.NewWithSource(opts.Region, opts.GuestIdentity.NodeName, types.DocumentSourceKataGuest)
	}
	return document.New(opts.Region)
}
