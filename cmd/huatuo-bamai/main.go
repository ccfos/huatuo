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
	tracer  *tracing.TracingManager
}

func NewDaemon(opts *Options) *Daemon {
	return &Daemon{opts: opts}
}

// Run brings the daemon up by calling each module's setup function in
// order, recording its cleanup on a stack, then blocks until a termination
// signal arrives and runs the stack in reverse. A setup failure tears
// down whatever already came up before returning the original error.
func (d *Daemon) Run(ctx context.Context) error {
	var cleanups []func(context.Context) error

	shutdown := func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		var errs []error
		for i := len(cleanups) - 1; i >= 0; i-- {
			if err := cleanups[i](shutdownCtx); err != nil {
				errs = append(errs, err)
			}
		}

		return errors.Join(errs...)
	}

	run := func(name string, setup func(*Daemon) (func(context.Context) error, error)) error {
		cleanup, err := setup(d)
		if err != nil {
			_ = shutdown()
			return fmt.Errorf("%s: %w", name, err)
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}

		return nil
	}

	steps := []struct {
		name  string
		setup func(*Daemon) (func(context.Context) error, error)
	}{
		{"pidfile", lockPidfile},
		{"cgroup", setupCgroup},
		{"storage", setupStorage},
		{"bpf", setupBPF},
		{"pod", setupPodManager},
		{"metrics", setupMetrics},
		{"toolstream", startToolstream},
		{"tracing", startTracing},
		{"handlers", startHandlers},
		{"cgroup-cpu-quota", applyCgroupCPUQuota},
	}
	for _, s := range steps {
		if err := run(s.name, s.setup); err != nil {
			return err
		}
	}

	log.Infof("huatuo-bamai started successfully")
	s := d.waitForSignal(ctx)
	log.Infof("huatuo-bamai received signal %v, shutting down", s)

	if err := shutdown(); err != nil {
		log.Warnf("shutdown completed with errors: %v", err)
	}

	return nil
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
