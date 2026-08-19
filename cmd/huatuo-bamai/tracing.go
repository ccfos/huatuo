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
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/tracing"
)

type toolstreamServer interface {
	QuiesceAndDrain(ctx context.Context) error
	Close() error
}

func setupBPF(_ *Daemon) (func(context.Context) error, error) {
	if err := bpf.Init(&bpf.Option{}); err != nil {
		return nil, fmt.Errorf("init bpf: %w", err)
	}

	return func(context.Context) error {
		bpf.Shutdown()
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

	return func(ctx context.Context) error {
		return closeToolstream(ctx, srv)
	}, nil
}

func closeToolstream(ctx context.Context, srv toolstreamServer) error {
	initialDrainCtx, cancelInitialDrain := initialToolstreamDrainContext(ctx)
	drainErr := srv.QuiesceAndDrain(initialDrainCtx)
	cancelInitialDrain()
	closeErr := srv.Close()
	if errors.Is(closeErr, toolstream.ErrHandlersActive) && ctx.Err() == nil {
		retryDrainErr := srv.QuiesceAndDrain(ctx)
		retryCloseErr := srv.Close()
		if retryDrainErr == nil && retryCloseErr == nil {
			return nil
		}
		drainErr = errors.Join(drainErr, retryDrainErr)
		closeErr = errors.Join(closeErr, retryCloseErr)
		if !errors.Is(retryCloseErr, toolstream.ErrHandlersActive) {
			return &cleanupBoundaryError{err: errors.Join(drainErr, closeErr)}
		}
	}

	err := errors.Join(drainErr, closeErr)
	if err == nil {
		return nil
	}

	// Close without ErrHandlersActive establishes that dispatch cannot enter a
	// handler again, so storage cleanup is safe even if draining timed out.
	var blocked cleanupDependency
	if errors.Is(closeErr, toolstream.ErrHandlersActive) {
		blocked = dependencyToolstreamStopped
	}
	return &cleanupBoundaryError{
		err:     err,
		blocked: blocked,
	}
}

func initialToolstreamDrainContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}

	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.WithCancel(ctx)
	}

	// Leave half of the cleanup budget for a handler that owns a decoded frame
	// after the initial drain force-closes idle connections.
	return context.WithTimeout(ctx, remaining/2)
}

func startTracing(d *Daemon) (func(context.Context) error, error) {
	mgr, err := tracing.NewManager(config.Get().BlackList)
	if err != nil {
		return nil, fmt.Errorf("new tracing manager: %w", err)
	}

	if err := mgr.Start(context.Background()); err != nil {
		return nil, fmt.Errorf("start tracing manager: %w", err)
	}

	d.tracer = mgr
	return func(ctx context.Context) error {
		if err := mgr.Close(ctx); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
		return nil
	}, nil
}

func startHandlers(d *Daemon) (func(context.Context) error, error) {
	handlers.Start(handlers.ServerOptions{
		Addr:           config.Get().HTTPServer.ListenAddress,
		TracingManager: d.tracer,
		PromReg:        d.metrics,
		VersionInfo:    &d.opts.VersionInfo,
	})
	return nil, nil
}
