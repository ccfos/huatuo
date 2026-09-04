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
	"time"

	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/nodeclient"
)

func setupJobManagers(ctx context.Context, d *Daemon) (func(context.Context) error, error) {
	client, err := nodeclient.New(&nodeclient.Config{
		Port:        d.opts.Config.Agent.HTTPPort,
		BearerToken: d.opts.Config.Agent.Auth.BearerToken,
		Observe:     d.agentObserver,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Node client: %w", err)
	}
	controller := d.opts.Config.Jobs.Controller
	manager, err := job.NewManager(ctx, client, job.ManagerConfig{
		StoreDSN: d.opts.Config.Jobs.StoreDSN,
		ProfilingPolicy: job.Policy{
			MaxJobsPerHost: d.opts.Config.Jobs.Profiling.MaxConcurrentPerHost,
			MaxTotalJobs:   d.opts.Config.Jobs.Profiling.MaxConcurrent,
		},
		TracingPolicy: job.Policy{
			MaxJobsPerHost: d.opts.Config.Jobs.Tracing.MaxConcurrentPerHost,
			MaxTotalJobs:   d.opts.Config.Jobs.Tracing.MaxConcurrent,
		},
		StatusPollInterval: time.Duration(
			controller.StatusPollIntervalSeconds,
		) * time.Second,
		PendingTimeout: time.Duration(controller.PendingTimeoutSeconds) * time.Second,
		CompletionGracePeriod: time.Duration(
			controller.CompletionGracePeriodSeconds,
		) * time.Second,
		NodeUnavailableGracePeriod: time.Duration(
			controller.NodeUnavailableGracePeriodSeconds,
		) * time.Second,
		JobRetentionPeriod: time.Duration(controller.JobRetentionPeriodHours) * time.Hour,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize job manager: %w", err)
	}

	d.jobManager = manager
	d.metrics.MustRegister(newJobManagerCollector(manager))
	return func(ctx context.Context) error {
		return manager.Shutdown(ctx)
	}, nil
}
