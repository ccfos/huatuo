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

	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/profiling"
	"huatuo-bamai/internal/server"

	"golang.org/x/time/rate"
)

func startHandlers(_ context.Context, d *Daemon) (func(context.Context) error, error) {
	runningServer, err := handlers.Start(&handlers.ServerOptions{
		Addr:                d.opts.Config.APIServer.TCPAddr,
		PromReg:             d.metrics,
		TraceJobManager:     d.jobManager,
		ProfilingJobManager: d.jobManager,
		ProfileService:      d.profileService,
		ProfilingConfig: profiling.Config{
			AggregationInterval: d.opts.Config.Profiling.AggregationInterval,
			ExecutionTimeout:    d.opts.Config.Profiling.ExecutionTimeout,
			MaxProfilerProcs:    d.opts.Config.Profiling.MaxProfilerProcs,
			FlameGraphBaseURL:   d.opts.Config.Profiling.FlameGraphBaseURL,
		},
		AuthUsers:         authUsers(d.opts.Config.Auth.Users),
		EnablePProf:       d.opts.EnablePProf,
		VersionInfo:       &d.opts.VersionInfo,
		RateLimit:         rate.Limit(d.opts.Config.APIServer.RateLimit),
		RateBurst:         d.opts.Config.APIServer.RateBurst,
		ReadHeaderTimeout: time.Duration(d.opts.Config.APIServer.ReadHeaderTimeoutSeconds) * time.Second,
		ReadTimeout:       time.Duration(d.opts.Config.APIServer.ReadTimeoutSeconds) * time.Second,
		WriteTimeout:      time.Duration(d.opts.Config.APIServer.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:       time.Duration(d.opts.Config.APIServer.IdleTimeoutSeconds) * time.Second,
		MaxHeaderBytes:    d.opts.Config.APIServer.MaxHeaderBytes,
		MaxBodyBytes:      d.opts.Config.APIServer.MaxBodyBytes,
		Ready: func(ctx context.Context) error {
			return errors.Join(d.jobManager.Ready(ctx), d.profileService.Ready(ctx))
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
			Name:        user.Name,
			Permissions: user.Permissions,
			IsAdmin:     user.IsAdmin,
		})
	}
	return result
}
