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
	"errors"
	"net"
	"time"

	"huatuo-bamai/cmd/huatuo-apiserver/handlers/profiling"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/trace"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/version"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

// ServerOptions groups the dependencies required to start the API server.
type ServerOptions struct {
	Addr                string
	PromReg             *prometheus.Registry
	TraceJobManager     trace.JobManager
	ProfilingJobManager profiling.JobManager
	ProfileService      profiling.ProfileQueryService
	ProfilingConfig     profiling.Config
	AuthUsers           []server.UserConfig
	EnablePProf         bool
	VersionInfo         *version.Info
	RateLimit           rate.Limit
	RateBurst           int
	ReadHeaderTimeout   time.Duration
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	MaxHeaderBytes      int
	MaxBodyBytes        int64
	Ready               func(context.Context) error
}

// RunningServer exposes the lifecycle of the API listener.
type RunningServer interface {
	Shutdown(ctx context.Context) error
	Done() <-chan struct{}
	Wait(ctx context.Context) error
	Addr() net.Addr
}

// Start starts the API service with the given configuration.
func Start(opts *ServerOptions) (RunningServer, error) {
	if opts == nil {
		return nil, errors.New("start API server: options are required")
	}
	if opts.TraceJobManager == nil || opts.ProfilingJobManager == nil {
		return nil, errors.New("start API server: job managers are required")
	}
	if opts.ProfileService == nil {
		return nil, errors.New("start API server: profile service is required")
	}
	httpServer := server.NewServer(&server.Config{
		RequireAuth:     true,
		EnablePProf:     opts.EnablePProf,
		EnableRateLimit: true,
		RateLimit:       opts.RateLimit,
		RateBurst:       opts.RateBurst,
		AuthUsers:       opts.AuthUsers,
		AdminPaths: []string{
			"/v1/profiles/flamegraph/**",
		},
		PromReg:           opts.PromReg,
		VersionInfo:       opts.VersionInfo,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		MaxHeaderBytes:    opts.MaxHeaderBytes,
		MaxBodyBytes:      opts.MaxBodyBytes,
		Ready:             opts.Ready,
	})

	// Register trace routes
	httpServer.MustRegisterRoutes("/v1/traces", trace.NewHandler(opts.TraceJobManager).Handlers)
	httpServer.MustRegisterRoutes(
		"/v1/profiles",
		profiling.NewHandler(opts.ProfilingJobManager, opts.ProfileService, opts.ProfilingConfig).Handlers,
	)

	if err := httpServer.Start(opts.Addr); err != nil {
		return nil, err
	}

	return httpServer, nil
}
