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

	"huatuo-bamai/internal/job"
)

func setupJobManagers(d *Daemon) (func(context.Context) error, error) {
	nodeAgent := job.NewHTTPNodeAgent()
	profilingPolicy := job.TypePolicy{
		Group:          "profiling",
		MaxJobsPerHost: d.opts.Config.TaskConfig.MaxProfilingTasksPerHost,
		MaxTotalJobs:   d.opts.Config.TaskConfig.MaxTotalProfilingTasks,
	}
	tracingPolicy := job.TypePolicy{
		Group:          "tracing",
		MaxJobsPerHost: d.opts.Config.TaskConfig.MaxTracingTasksPerHost,
		MaxTotalJobs:   d.opts.Config.TaskConfig.MaxTotalTracingTasks,
	}
	manager, err := job.NewManager(d.ctx, nodeAgent, job.ManagerConfig{
		StoreDSN:            d.opts.Config.TaskConfig.JobStoreDSN,
		ShutdownConcurrency: d.opts.Config.TaskConfig.ShutdownConcurrency,
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
	return func(ctx context.Context) error {
		return manager.ShutdownContext(ctx)
	}, nil
}
