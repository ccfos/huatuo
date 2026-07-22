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
	"os"
	"os/signal"
	"syscall"
	"time"

	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pidfile"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/version"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	appName  = "huatuo-apiserver"
	appUsage = "Control-plane API server for orchestrating HuaTuo profiling and tracing jobs across hosts"
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
		log.WithError(err).Error("app run failed")
		os.Exit(1)
	}
}

func mainAction(opts *Options) error {
	return NewDaemon(opts).Run(context.Background())
}

type setupFunc func(context.Context, *Daemon) (func(context.Context) error, error)

type daemonStep struct {
	name  string
	setup setupFunc
}

// Daemon owns handles produced by startup steps and consumed by later steps.
type Daemon struct {
	opts *Options

	metrics        *prometheus.Registry
	jobManager     *job.Manager
	profileService *profileService.Service
	agentObserver  job.AgentRequestObserver
	apiServer      interface {
		Done() <-chan struct{}
		Wait(ctx context.Context) error
	}
	steps []daemonStep
}

func NewDaemon(opts *Options) *Daemon {
	return &Daemon{
		opts: opts,
		steps: []daemonStep{
			{name: "pidfile", setup: lockPidfile},
			{name: "cgroup", setup: setupCgroup},
			{name: "profiling-flamegraph", setup: setupProfileFlamegraph},
			{name: "metrics", setup: setupMetrics},
			{name: "job-managers", setup: setupJobManagers},
			{name: "handlers", setup: startHandlers},
		},
	}
}

// Run starts each module in order and tears initialized modules down in reverse.
func (d *Daemon) Run(ctx context.Context) error {
	cleanups := make([]func(context.Context) error, 0, len(d.steps))
	shutdownTimeout := 60 * time.Second
	if d.opts != nil && d.opts.Config != nil && d.opts.Config.APIServer.ShutdownTimeoutSeconds > 0 {
		shutdownTimeout = time.Duration(d.opts.Config.APIServer.ShutdownTimeoutSeconds) * time.Second
	}

	shutdown := func() error {
		shutdownCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			shutdownTimeout,
		)
		defer cancel()

		var errs []error
		for i := len(cleanups) - 1; i >= 0; i-- {
			remainingSteps := i + 1
			shutdownDeadline, _ := shutdownCtx.Deadline()
			remaining := time.Until(shutdownDeadline)
			if remaining <= 0 {
				remaining = time.Nanosecond
			}
			stepCtx, stepCancel := context.WithTimeout(
				context.WithoutCancel(shutdownCtx),
				remaining/time.Duration(remainingSteps),
			)
			if err := cleanups[i](stepCtx); err != nil {
				errs = append(errs, err)
			}
			stepCancel()
		}

		return errors.Join(errs...)
	}

	for _, step := range d.steps {
		cleanup, err := step.setup(ctx, d)
		if err != nil {
			if cleanupErr := shutdown(); cleanupErr != nil {
				log.WithError(cleanupErr).Warn("startup rollback completed with errors")
			}
			return fmt.Errorf("%s: %w", step.name, err)
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}

	log.Info("huatuo-apiserver started")
	s, serveErr := d.waitForSignal(ctx)
	if serveErr != nil {
		log.WithError(serveErr).Error("api server stopped unexpectedly")
	} else {
		log.WithField("signal", s).Info("huatuo-apiserver shutting down")
	}

	if err := errors.Join(serveErr, shutdown()); err != nil {
		log.WithError(err).Warn("shutdown completed with errors")
		return err
	}

	return nil
}

func (d *Daemon) waitForSignal(ctx context.Context) (os.Signal, error) {
	waitCh := make(chan os.Signal, 1)
	signal.Notify(
		waitCh,
		syscall.SIGHUP,
		syscall.SIGQUIT,
		syscall.SIGUSR1,
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer signal.Stop(waitCh)

	if d.apiServer == nil {
		select {
		case <-ctx.Done():
			return nil, nil
		case s := <-waitCh:
			return s, nil
		}
	}

	select {
	case <-ctx.Done():
		return nil, nil
	case s := <-waitCh:
		return s, nil
	case <-d.apiServer.Done():
		return nil, d.apiServer.Wait(context.WithoutCancel(ctx))
	}
}

func lockPidfile(_ context.Context, _ *Daemon) (func(context.Context) error, error) {
	lk, err := pidfile.Lock(appName)
	if err != nil {
		return nil, fmt.Errorf("lock pid file: %w", err)
	}

	return func(context.Context) error {
		lk.Unlock()
		return nil
	}, nil
}
