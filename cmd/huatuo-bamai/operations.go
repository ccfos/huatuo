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
	"net"
	"path/filepath"
	"time"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/nodeagent/operation"
	nodeprofiling "github.com/ccfos/huatuo/internal/nodeagent/profiling"
	nodetracing "github.com/ccfos/huatuo/internal/nodeagent/tracing"
	"github.com/ccfos/huatuo/internal/toolstream"
)

func startOperations(d *Daemon) (func(context.Context) error, error) {
	cfg := config.Get()
	nodeAPIAddress, err := localAPIAddress(cfg.HTTPServer.ListenAddress)
	if err != nil {
		return nil, err
	}

	manager, err := operation.NewManager(operation.Config{
		MaxConcurrent: cfg.Operations.MaxConcurrent,
		Lifecycle: operation.LifecyclePolicy{
			LaunchTimeout: time.Duration(cfg.Operations.LaunchTimeoutSeconds) *
				time.Second,
			StopGracePeriod: time.Duration(cfg.Operations.StopGracePeriodSeconds) *
				time.Second,
			FinalizationTimeout: time.Duration(cfg.Operations.FinalizationTimeoutSeconds) *
				time.Second,
			TerminalRetentionPeriod: time.Duration(cfg.Operations.TerminalRetentionPeriodSeconds) *
				time.Second,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create operation manager: %w", err)
	}
	initialized := false
	defer func() {
		if !initialized {
			_ = manager.Shutdown(context.Background())
		}
	}()

	profilingService, err := nodeprofiling.NewService(manager, &nodeprofiling.Config{
		ProfilerPath:         filepath.Join(d.opts.ToolBinDir, "profiler"),
		ToolstreamSocketPath: toolstream.DefaultSockPath,
		NodeAPIAddress:       nodeAPIAddress,
		JavaToolPath:         cfg.Profiling.JavaToolPath,
		PythonToolPath:       cfg.Profiling.PythonToolPath,
		AggregationInterval: time.Duration(cfg.Profiling.AggregationIntervalSeconds) *
			time.Second,
		MaxConcurrentProcesses:  cfg.Profiling.MaxConcurrentProcesses,
		CommandOutputLimitBytes: cfg.Profiling.CommandOutputLimitBytes,
		ToolstreamServer:        d.toolstreamServer,
		ResultPublisher:         d.publications,
	})
	if err != nil {
		return nil, fmt.Errorf("create profiling operation service: %w", err)
	}
	tracingService, err := nodetracing.NewService(manager)
	if err != nil {
		return nil, fmt.Errorf("create tracing operation service: %w", err)
	}

	d.operationManager = manager
	d.profilingService = profilingService
	d.tracingService = tracingService
	initialized = true
	return manager.Shutdown, nil
}

func localAPIAddress(listenAddress string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return "", fmt.Errorf("derive local Node API address from %q: %w", listenAddress, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}
