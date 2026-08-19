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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pidfile"
	"huatuo-bamai/internal/version"
	"huatuo-bamai/pkg/tracing"

	"github.com/prometheus/client_golang/prometheus"

	_ "huatuo-bamai/core/autotracing"
	_ "huatuo-bamai/core/events"
	_ "huatuo-bamai/core/metrics"
)

const (
	appName  = "huatuo-bamai"
	appUsage = "Node agent for Linux kernel observability"

	shutdownTimeout = 10 * time.Second
)

var (
	// AppGitCommit is the source revision the binary was built from, set by Makefile.
	AppGitCommit string
	// AppBuildTime is the build timestamp, set by Makefile.
	AppBuildTime string
	// AppVersion is the release version read from the VERSION file, set by Makefile.
	AppVersion string
)

func main() {
	app := buildCommand(version.Seed{
		Name:      appName,
		Version:   AppVersion,
		GitCommit: AppGitCommit,
		BuildTime: AppBuildTime,
	})

	if err := app.Run(os.Args); err != nil {
		log.Errorf("app run: %v", err)
		os.Exit(1)
	}
}

func mainAction(opts *Options) error {
	return NewDaemon(opts).Run(context.Background())
}

// Daemon owns handles that earlier setup steps write and later ones read
// (e.g. cgr produced by setupCgroup, consumed by applyCgroupCPUQuota).
type Daemon struct {
	opts *Options

	cgr     cgroups.Cgroup
	metrics *prometheus.Registry
	tracer  *tracing.Manager
}

type cleanupStep struct {
	name               string
	cleanup            func(context.Context) error
	requires           cleanupDependency
	blocksOnIncomplete cleanupDependency
}

type cleanupDependency uint8

const (
	dependencyTracingStopped cleanupDependency = 1 << iota
	dependencyToolstreamStopped
)

type daemonSetupStep struct {
	name               string
	setup              func(*Daemon) (func(context.Context) error, error)
	requires           cleanupDependency
	blocksOnIncomplete cleanupDependency
}

// cleanupBoundaryError records which later cleanup is unsafe after a failed
// cleanup. Its Error omits this internal control state.
type cleanupBoundaryError struct {
	err     error
	blocked cleanupDependency
}

func (e *cleanupBoundaryError) Error() string { return e.err.Error() }
func (e *cleanupBoundaryError) Unwrap() error { return e.err }
func (e *cleanupBoundaryError) blockedCleanupDependencies() cleanupDependency {
	return e.blocked
}

func NewDaemon(opts *Options) *Daemon {
	return &Daemon{opts: opts}
}

// Run brings the daemon up by calling each module's setup function in
// order, recording its cleanup on a stack, then blocks until a termination
// signal arrives and runs the stack in reverse. A setup failure tears
// down whatever already came up before returning the original error.
func (d *Daemon) Run(ctx context.Context) error {
	var cleanups []cleanupStep

	steps := []daemonSetupStep{
		{name: "pidfile", setup: lockPidfile},
		{name: "cgroup", setup: setupCgroup},
		{
			name:     "storage",
			setup:    setupStorage,
			requires: dependencyTracingStopped | dependencyToolstreamStopped,
		},
		{name: "bpf", setup: setupBPF, requires: dependencyTracingStopped},
		{name: "pod", setup: setupPodManager, requires: dependencyTracingStopped},
		{name: "metrics", setup: setupMetrics},
		{
			name:               "toolstream",
			setup:              startToolstream,
			requires:           dependencyTracingStopped,
			blocksOnIncomplete: dependencyToolstreamStopped,
		},
		{
			name:               "tracing",
			setup:              startTracing,
			blocksOnIncomplete: dependencyTracingStopped,
		},
		{name: "handlers", setup: startHandlers},
		{name: "cgroup-cpu-quota", setup: applyCgroupCPUQuota},
	}
	for _, step := range steps {
		if err := runSetupStep(
			ctx,
			d,
			&cleanups,
			step,
			shutdownTimeout,
		); err != nil {
			return err
		}
	}

	log.Infof("huatuo-bamai started successfully")
	s := d.waitForSignal(ctx)
	log.Infof("huatuo-bamai received signal %v, shutting down", s)

	if err := runCleanups(ctx, cleanups, shutdownTimeout); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return nil
}

func runSetupStep(
	ctx context.Context,
	d *Daemon,
	cleanups *[]cleanupStep,
	step daemonSetupStep,
	rollbackTimeout time.Duration,
) error {
	cleanup, setupErr := step.setup(d)
	if setupErr != nil {
		rollbackErr := runCleanups(ctx, *cleanups, rollbackTimeout)
		return errors.Join(
			fmt.Errorf("%s: %w", step.name, setupErr),
			rollbackErr,
		)
	}
	if cleanup != nil {
		*cleanups = append(*cleanups, cleanupStep{
			name:               step.name,
			cleanup:            cleanup,
			requires:           step.requires,
			blocksOnIncomplete: step.blocksOnIncomplete,
		})
	}

	return nil
}

func runCleanups(ctx context.Context, cleanups []cleanupStep, stepTimeout time.Duration) error {
	var errs []error
	var blocked cleanupDependency

	for i := len(cleanups) - 1; i >= 0; i-- {
		step := cleanups[i]
		if step.requires&blocked != 0 {
			continue
		}
		stepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stepTimeout)
		err := step.cleanup(stepCtx)
		incomplete := stepCtx.Err() != nil
		cancel()
		if err == nil {
			continue
		}

		errs = append(errs, fmt.Errorf("%s cleanup: %w", step.name, err))
		var boundary interface {
			blockedCleanupDependencies() cleanupDependency
		}
		if errors.As(err, &boundary) {
			blocked |= boundary.blockedCleanupDependencies()
			continue
		}
		if incomplete {
			blocked |= step.blocksOnIncomplete
		}
	}

	return errors.Join(errs...)
}

func (d *Daemon) waitForSignal(ctx context.Context) os.Signal {
	waitCh := make(chan os.Signal, 1)
	signal.Notify(waitCh, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM)

	if d.opts.DryRun {
		time.Sleep(2 * time.Second)
		log.Infof("dry-run complete, sending SIGTERM to self")
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	}

	select {
	case <-ctx.Done():
		return nil
	case s := <-waitCh:
		return s
	}
}

func lockPidfile(_ *Daemon) (func(context.Context) error, error) {
	lk, err := pidfile.Lock(appName)
	if err != nil {
		return nil, fmt.Errorf("lock pid file: %w", err)
	}

	return func(context.Context) error {
		lk.Unlock()
		return nil
	}, nil
}
