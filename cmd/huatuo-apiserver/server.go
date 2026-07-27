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

	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/profiling"
	"huatuo-bamai/internal/server"

	"golang.org/x/time/rate"
)

func startHandlers(_ context.Context, d *Daemon) (func(context.Context) error, error) {
	var profileQueryService profiling.ProfileQueryService
	if d.profileService != nil {
		profileQueryService = d.profileService
	}

	runningServer, err := handlers.Start(&handlers.ServerOptions{
		Addr:                d.opts.Config.APIServer.ListenAddress,
		PromReg:             d.metrics,
		TraceJobManager:     d.jobManager,
		ProfilingJobManager: d.jobManager,
		ProfileService:      profileQueryService,
		ProfilingConfig: profiling.Config{
			AggregationIntervalSeconds:     d.opts.Config.Profiling.AggregationIntervalSeconds,
			MaxConcurrentProfilerProcesses: d.opts.Config.Profiling.MaxConcurrentProfilerProcesses,
			DashboardBaseURL:               d.opts.Config.Profiling.DashboardBaseURL,
		},
		AuthUsers:   authUsers(d.opts.Config.Auth.Users),
		EnablePProf: d.opts.EnablePProf,
		VersionInfo: &d.opts.VersionInfo,
		RateLimit:   rate.Limit(d.opts.Config.APIServer.RateLimit.RequestsPerSecond),
		RateBurst:   d.opts.Config.APIServer.RateLimit.Burst,
		Ready: func(ctx context.Context) error {
			err := d.jobManager.Ready(ctx)
			if d.profileService == nil {
				return err
			}
			return errors.Join(err, d.profileService.Ready(ctx))
		},
	})
	if err != nil {
		return nil, fmt.Errorf("start api server: %w", err)
	}
	d.apiServer = runningServer

	return runningServer.Shutdown, nil
}

func authUsers(users []config.UserConfig) []server.UserConfig {
	result := make([]server.UserConfig, 0, len(users))
	for _, user := range users {
		result = append(result, server.UserConfig{
			ID:          user.ID,
			BearerToken: user.BearerToken,
			Permissions: user.Permissions,
			IsAdmin:     user.Admin,
		})
	}
	return result
}
