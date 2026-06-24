// Copyright 2025 The HuaTuo Authors
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
	"time"

	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/profiling"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/trace"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/version"

	"github.com/prometheus/client_golang/prometheus"
)

// ServerStart starts the API service with the given configuration.
func ServerStart(addr string, promReg *prometheus.Registry, profilingManager, tracingManager *job.Manager, versionInfo version.Info) error {
	httpServer := server.NewServer(&server.Config{
		EnablePProf:     false,
		EnableRateLimit: false,
		AuthUsers:       getUserConfigs(),
		PromReg:         promReg,
		VersionInfo:     &versionInfo,
	})

	// Register trace routes
	httpServer.MustRegisterRoutes("/v1/traces", trace.NewHandler(tracingManager).Handlers)
	httpServer.MustRegisterRoutes("/v1/profiles", profiling.NewHandler(profilingManager).Handlers)

	_ = httpServer.Run(&server.Option{
		Addr:          addr,
		RetryMaxTime:  5 * time.Minute,
		RetryInterval: 1 * time.Minute,
	})

	return nil
}

// getUserConfigs converts apiserver config users to server.UserConfig.
func getUserConfigs() []server.UserConfig {
	cfg := config.Get()
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
