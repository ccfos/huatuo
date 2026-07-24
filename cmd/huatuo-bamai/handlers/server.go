// Copyright 2025, 2026 The HuaTuo Authors
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

package handlers

import (
	"context"
	"net"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/version"
	"huatuo-bamai/pkg/tracing"

	"github.com/prometheus/client_golang/prometheus"
)

// ServerOptions groups the dependencies required to start the HTTP server.
type ServerOptions struct {
	Addr           string
	TracingManager *tracing.Manager
	PromReg        *prometheus.Registry
	VersionInfo    *version.Info
}

// RunningServer exposes the lifecycle of the HTTP listener.
type RunningServer interface {
	Shutdown(ctx context.Context) error
	Done() <-chan struct{}
	Wait(ctx context.Context) error
	Addr() net.Addr
}

// Start starts the HTTP server with all handlers registered.
func Start(opts ServerOptions) (RunningServer, error) {
	s := server.NewServer(&server.Config{
		EnablePProf:     true,
		EnableRateLimit: true,
		RateLimit:       200,
		RateBurst:       200,
		PromReg:         opts.PromReg,
		VersionInfo:     opts.VersionInfo,
	})

	SetTracingManager(opts.TracingManager)

	s.MustRegisterRoutes("/tasks", NewTaskHandler().Handlers)
	s.MustRegisterRoutes("/tracers", NewTracerHandler(opts.TracingManager).Handlers)
	s.MustRegisterRoutes("", NewContainerHandler().Handlers)
	s.MustRegisterRoutes("", NewConfigHandler().Handlers)
	evtCfg := config.Get().EventsWatch
	s.MustRegisterRoutes("/v1/events", NewEventsHandler(evtCfg.MaxClients, evtCfg.KeepAliveInterval).Handlers)

	if err := s.Start(opts.Addr); err != nil {
		return nil, err
	}

	return s, nil
}
