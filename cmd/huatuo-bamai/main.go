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

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pidfile"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/tracing"

	"github.com/prometheus/client_golang/prometheus"

	_ "huatuo-bamai/core/autotracing"
	_ "huatuo-bamai/core/events"
	_ "huatuo-bamai/core/metrics"
)

const (
	appName  = "huatuo-bamai"
	appUsage = "An In-depth Observation of Linux Kernel Application"

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
	if AppVersion == "" {
		log.Error("AppVersion is empty; binary must be linked with -ldflags \"-X main.AppVersion=...\"")
		os.Exit(1)
	}

	app := buildCommand(buildInfo{
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

// Daemon owns the process-wide lifecycle. Each field is the handle to one
// startup phase; a nil field means the phase is not yet active (or has been
// disabled by an Option), so Shutdown can be invoked at any point — including
// after a partial startup failure — and nil-guards each tear-down step.
type Daemon struct {
	opts *Options

	cgroup    cgroups.Cgroup
	pidLocked bool
	bpfReady  bool
	podReady  bool
	metrics   *prometheus.Registry
	tools     *toolstream.Server
	tracer    *tracing.TracingManager
}

func NewDaemon(opts *Options) *Daemon {
	return &Daemon{opts: opts}
}

// Run executes the startup pipeline, blocks until a termination signal (or
// ctx cancellation) arrives, then runs Shutdown. A startup failure short-
// circuits to a best-effort Shutdown before returning the original error.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.start(); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = d.Shutdown(shutdownCtx)

		return err
	}

	log.Infof("huatuo-bamai started successfully")
	s := d.waitForSignal(ctx)
	log.Infof("huatuo-bamai received signal %v, shutting down", s)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := d.Shutdown(shutdownCtx); err != nil {
		log.Warnf("shutdown completed with errors: %v", err)
	}

	return nil
}

// Shutdown tears down every active phase in reverse startup order. Each
// branch is nil-guarded so Shutdown is safe to call after a partial
// startup; errors are aggregated via errors.Join so a single failing
// phase does not skip the rest.
func (d *Daemon) Shutdown(ctx context.Context) error {
	var errs []error

	if d.tracer != nil {
		if err := d.tracer.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("tracing stop: %w", err))
		}
		// Drain bulk-buffered tracing writes after collectors stop and
		// before BPF teardown — bounded to keep shutdown predictable.
		if err := tracing.CloseStores(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close tracing stores: %w", err))
		}
		d.tracer = nil
	}

	if d.tools != nil {
		if err := d.tools.Close(); err != nil {
			errs = append(errs, fmt.Errorf("toolstream close: %w", err))
		}
		d.tools = nil
	}

	if d.podReady {
		pod.ManagerRelease()
		d.podReady = false
	}

	if d.bpfReady {
		bpf.Close()
		d.bpfReady = false
	}

	if d.cgroup != nil {
		if err := d.cgroup.DeleteRuntime(); err != nil {
			errs = append(errs, fmt.Errorf("cgroup delete: %w", err))
		}
		d.cgroup = nil
	}

	if d.pidLocked {
		pidfile.UnLock(appName)
		d.pidLocked = false
	}

	return errors.Join(errs...)
}

func (d *Daemon) start() error {
	steps := []func() error{
		d.lockPidfile,
		d.setupCgroup,
		d.setupStorage,
		d.setupBPF,
		d.setupPodManager,
		d.setupMetrics,
		d.startToolstream,
		d.startTracing,
		d.startHandlers,
		d.applyCgroupCPUQuota,
	}

	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
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

func (d *Daemon) lockPidfile() error {
	if err := pidfile.Lock(appName); err != nil {
		return fmt.Errorf("failed to lock pid file: %w", err)
	}
	d.pidLocked = true

	return nil
}
