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
	"net/http"

	"github.com/ccfos/huatuo/cmd/huatuo-apiserver/config"
	"github.com/ccfos/huatuo/cmd/huatuo-apiserver/handlers"
	"github.com/ccfos/huatuo/cmd/huatuo-apiserver/handlers/profiling"
	"github.com/ccfos/huatuo/internal/httpserver"
)

func startHandlers(ctx context.Context, d *Daemon) (func(context.Context) error, error) {
	mcpCleanup, err := setupMCP(ctx, d)
	if err != nil {
		return nil, err
	}
	var mcpHandler http.Handler
	if d.mcpHandler != nil {
		mcpHandler = d.mcpHandler
	}
	runningServer, err := handlers.Start(&handlers.ServerOptions{
		Addr:                d.opts.Config.APIServer.ListenAddress,
		PromReg:             d.metrics,
		JobManager:          d.jobManager,
		ProfileStorage:      d.profileStorage,
		ProfileQueryService: d.profileQueryService,
		ProfilingConfig: profiling.Config{
			DashboardBaseURL: d.opts.Config.Profiling.DashboardBaseURL,
		},
		AuthUsers:   authUsers(d.opts.Config.Auth.Users),
		EnablePProf: d.opts.EnablePProf,
		VersionInfo: &d.opts.VersionInfo,
		RateLimit: &httpserver.RateLimitConfig{
			RequestsPerSecond: d.opts.Config.APIServer.RateLimit.RequestsPerSecond,
			Burst:             d.opts.Config.APIServer.RateLimit.Burst,
		},
		MCPHandler: mcpHandler,
	})
	if err != nil {
		var cleanupErr error
		if mcpCleanup != nil {
			cleanupErr = mcpCleanup(context.WithoutCancel(ctx))
		}
		return nil, errors.Join(fmt.Errorf("start api server: %w", err), cleanupErr)
	}

	d.apiServer = runningServer

	return func(ctx context.Context) error {
		var mcpErr error
		if mcpCleanup != nil {
			// Cancel MCP streams before waiting for the shared HTTP server to drain.
			mcpErr = mcpCleanup(ctx)
		}
		return errors.Join(mcpErr, runningServer.Shutdown(ctx))
	}, nil
}

func authUsers(users []config.UserConfig) []httpserver.UserConfig {
	result := make([]httpserver.UserConfig, 0, len(users))
	for _, user := range users {
		result = append(result, httpserver.UserConfig{
			ID:          user.ID,
			BearerToken: user.BearerToken,
			Permissions: user.Permissions,
			IsAdmin:     user.Admin,
		})
	}
	return result
}
