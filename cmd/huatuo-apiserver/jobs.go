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
)

func setupJobManagers(ctx context.Context, d *Daemon) (func(context.Context) error, error) {
	nodeAgent := job.NewHTTPNodeAgent(job.HTTPNodeAgentConfig{
		Port:           d.opts.Config.Agent.HTTPPort,
		RequestTimeout: time.Duration(d.opts.Config.Agent.RequestTimeoutSeconds) * time.Second,
		Observe:        d.agentObserver,
	})
	profilingPolicy := job.TypePolicy{
		Group:          "profiling",
		MaxJobsPerHost: d.opts.Config.Jobs.Profiling.MaxConcurrentPerHost,
		MaxTotalJobs:   d.opts.Config.Jobs.Profiling.MaxConcurrent,
	}
	tracingPolicy := job.TypePolicy{
		Group:          "tracing",
		MaxJobsPerHost: d.opts.Config.Jobs.Tracing.MaxConcurrentPerHost,
		MaxTotalJobs:   d.opts.Config.Jobs.Tracing.MaxConcurrent,
	}
	manager, err := job.NewManager(ctx, nodeAgent, job.ManagerConfig{
		StoreDSN: d.opts.Config.Jobs.StoreDSN,
		StatusPollInterval: time.Duration(
			d.opts.Config.Agent.StatusPollingIntervalSeconds,
		) * time.Second,
		MaxConsecutivePollErrors: d.opts.Config.Agent.MaxConsecutiveStatusPollingErrors,
		TypePolicies: map[job.JobType]job.TypePolicy{
			job.JobTypeProfilingCPU:    profilingPolicy,
			job.JobTypeProfilingMemory: profilingPolicy,
			job.JobTypeTracing:         tracingPolicy,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize job manager: %w", err)
	}

	d.jobManager = manager
	d.metrics.MustRegister(newJobManagerCollector(manager))
	return func(ctx context.Context) error {
		return manager.ShutdownContext(ctx)
	}, nil
}
