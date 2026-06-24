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
	"huatuo-bamai/cmd/huatuo-bamai/handlers"
	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/tracing"
)

func setupBPF(_ *Daemon) (func(context.Context) error, error) {
	if err := bpf.NewManager(&bpf.Option{}); err != nil {
		return nil, fmt.Errorf("init bpf manager: %w", err)
	}

	return func(context.Context) error {
		bpf.Close()
		return nil
	}, nil
}

func startToolstream(_ *Daemon) (func(context.Context) error, error) {
	srv, err := toolstream.NewServerDefault()
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	return func(context.Context) error { return srv.Close() }, nil
}

func startTracing(d *Daemon) (func(context.Context) error, error) {
	mgr, err := tracing.NewManager(config.Get().BlackList)
	if err != nil {
		return nil, fmt.Errorf("new tracing manager: %w", err)
	}

	if err := mgr.Start(); err != nil {
		return nil, fmt.Errorf("start tracing manager: %w", err)
	}

	d.tracer = mgr
	// Stop collectors first, then drain bulk-buffered writes before BPF teardown.
	return func(ctx context.Context) error {
		if err := mgr.Stop(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		if err := tracing.CloseStores(ctx); err != nil {
			return fmt.Errorf("close stores: %w", err)
		}
		return nil
	}, nil
}

func startHandlers(d *Daemon) (func(context.Context) error, error) {
	handlers.Start(config.Get().APIServer.TCPAddr, d.tracer, d.metrics, d.opts.VersionInfo)
	return nil, nil
}
