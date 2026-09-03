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
	"time"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/cmd/huatuo-bamai/handlers"
	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/document"
	"huatuo-bamai/internal/profiling"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/internal/tracing"
	"huatuo-bamai/pkg/types"
)

const defaultHTTPDrainTimeout = 5 * time.Second

func setupBPF(_ *Daemon) (func(context.Context) error, error) {
	if err := bpf.Init(&bpf.Option{}); err != nil {
		return nil, fmt.Errorf("init bpf: %w", err)
	}

	return func(context.Context) error {
		bpf.Shutdown()
		return nil
	}, nil
}

func startToolstream(d *Daemon) (func(context.Context) error, error) {
	srv, err := toolstream.NewServerDefault()
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	if d.profileStore != nil {
		documentWriter, err := profiling.NewDocumentWriter(
			d.profileStore,
			document.New(d.opts.Region),
		)
		if err != nil {
			return nil, err
		}
		toolstream.Register(srv, types.ProfilingToolName, documentWriter.Write)
	}

	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	d.toolstreamServer = srv

	return func(context.Context) error { return srv.Close() }, nil
}

func startTracing(d *Daemon) (func(context.Context) error, error) {
	mgr, err := tracing.NewManager(config.Get().BlackList)
	if err != nil {
		return nil, fmt.Errorf("new tracing manager: %w", err)
	}

	if err := mgr.Start(context.Background()); err != nil {
		return nil, fmt.Errorf("start tracing manager: %w", err)
	}

	handlers.SetTracingManager(mgr)
	return func(ctx context.Context) error {
		if err := mgr.Close(ctx); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		return nil
	}, nil
}

func startHandlers(d *Daemon) (func(context.Context) error, error) {
	httpConfig := config.Get().HTTPServer
	runningServer, err := handlers.Start(handlers.ServerOptions{
		Addr:             httpConfig.ListenAddress,
		BearerToken:      httpConfig.Auth.BearerToken,
		ProfilingService: d.profilingService,
		TracingService:   d.tracingService,
		TracingStore:     d.tracingStore,
		PromReg:          d.metrics,
		VersionInfo:      &d.opts.VersionInfo,
	})
	if err != nil {
		return nil, fmt.Errorf("start handlers: %w", err)
	}
	d.apiServer = runningServer

	return func(ctx context.Context) error {
		// Admission closes before the listener so in-flight Start requests cannot
		// register work after shutdown begins.
		d.operationManager.BeginShutdown()
		drainCtx, cancel := context.WithTimeout(ctx, defaultHTTPDrainTimeout)
		drainErr := runningServer.Shutdown(drainCtx)
		cancel()
		if drainErr == nil {
			return nil
		}
		return errors.Join(
			fmt.Errorf("drain HTTP server: %w", drainErr),
			runningServer.Close(),
		)
	}, nil
}
