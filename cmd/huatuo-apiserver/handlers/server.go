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

	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/profiling"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/trace"
	"huatuo-bamai/internal/job"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/version"

	"github.com/prometheus/client_golang/prometheus"
)

// ServerOptions groups the dependencies required to start the API server.
type ServerOptions struct {
	Addr           string
	PromReg        *prometheus.Registry
	JobManager     *job.Manager
	ProfileService *profileService.Service
	EnablePProf    bool
	VersionInfo    *version.Info
	Config         *config.Config
}

// ServerStart starts the API service with the given configuration.
func ServerStart(opts ServerOptions) (func(context.Context) error, error) {
	if opts.Config == nil {
		return nil, errors.New("start API server: config is required")
	}
	if opts.JobManager == nil {
		return nil, errors.New("start API server: job manager is required")
	}
	if opts.ProfileService == nil {
		return nil, errors.New("start API server: profile service is required")
	}
	httpServer := server.NewServer(&server.Config{
		EnablePProf:     opts.EnablePProf,
		EnableRateLimit: true,
		RateLimit:       200,
		RateBurst:       200,
		AuthUsers:       getUserConfigs(opts.Config),
		PromReg:         opts.PromReg,
		VersionInfo:     opts.VersionInfo,
	})

	// Register trace routes
	httpServer.MustRegisterRoutes("/v1/traces", trace.NewHandler(opts.JobManager).Handlers)
	httpServer.MustRegisterRoutes(
		"/v1/profiles",
		profiling.NewHandler(opts.JobManager, opts.ProfileService, opts.Config.Profiling).Handlers,
	)

	if err := httpServer.Start(opts.Addr); err != nil {
		return nil, err
	}

	return httpServer.Shutdown, nil
}

// getUserConfigs converts apiserver config users to server.UserConfig.
func getUserConfigs(cfg *config.Config) []server.UserConfig {
	users := make([]server.UserConfig, 0, len(cfg.Auth.Users))

	for _, u := range cfg.Auth.Users {
		users = append(users, server.UserConfig{
			ID:          u.ID,
			Name:        u.Name,
			Permissions: u.Permissions,
			IsAdmin:     u.IsAdmin,
		})
	}

	return users
}
