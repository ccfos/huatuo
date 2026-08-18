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

	"huatuo-bamai/cmd/huatuo-apiserver/handlers/profiling"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/trace"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/version"

	"github.com/prometheus/client_golang/prometheus"
)

// ServerOptions groups the dependencies required to start the API server.
type ServerOptions struct {
	Addr            string
	PromReg         *prometheus.Registry
	JobManager      *job.Manager
	ProfileService  profiling.ProfileQueryService
	ProfilingConfig profiling.Config
	AuthUsers       []server.UserConfig
	EnablePProf     bool
	VersionInfo     *version.Info
	RateLimit       *server.RateLimitConfig
	Ready           func(context.Context) error
}

// Start starts the API service with the given configuration.
func Start(opts *ServerOptions) (*server.Server, error) {
	if opts == nil {
		return nil, errors.New("start API server: options are required")
	}
	if opts.JobManager == nil {
		return nil, errors.New("start API server: job manager is required")
	}
	httpServer := server.NewServer(&server.Config{
		RequireAuth: true,
		EnablePProf: opts.EnablePProf,
		RateLimit:   opts.RateLimit,
		AuthUsers:   opts.AuthUsers,
		AdminPaths: []string{
			"/v1/profiles/flamegraph/**",
		},
		PromReg:     opts.PromReg,
		VersionInfo: opts.VersionInfo,
		Ready:       opts.Ready,
	})

	// Register trace routes
	httpServer.MustRegisterRoutes(
		"/v1/traces",
		trace.NewHandler(opts.JobManager).Handlers,
	)
	profileHandlers := profiling.DisabledHandlers()
	if opts.ProfileService != nil {
		profileHandlers = profiling.NewHandler(
			opts.JobManager,
			opts.ProfileService,
			opts.ProfilingConfig,
		).Handlers
	}
	httpServer.MustRegisterRoutes("/v1/profiles", profileHandlers)

	if err := httpServer.Start(opts.Addr); err != nil {
		return nil, err
	}

	return httpServer, nil
}
