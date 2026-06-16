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
	"strings"
	"syscall"
	"time"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/cmd/huatuo-bamai/handlers"
	_ "huatuo-bamai/core/autotracing"
	_ "huatuo-bamai/core/events"
	_ "huatuo-bamai/core/metrics"
	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pidfile"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/storage/driver"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/tracing"

	"github.com/prometheus/client_golang/prometheus"
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
		log.Error("the value of AppVersion must be specified")
		os.Exit(1)
	}

	app := buildCommand(buildInfo{
		Version:   AppVersion,
		GitCommit: AppGitCommit,
		BuildTime: AppBuildTime,
	})

	if err := app.Run(os.Args); err != nil {
		log.Errorf("Error: %v", err)
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
	tracing   *tracing.TracingManager
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

	log.Infof("huatuo-bamai now starting success")
	s := d.waitForSignal(ctx)
	log.Infof("huatuo-bamai exited by signal %v", s)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := d.Shutdown(shutdownCtx); err != nil {
		log.Warnf("shutdown: %v", err)
	}

	return nil
}

// Shutdown tears down every active phase in reverse startup order. Each
// branch is nil-guarded so Shutdown is safe to call after a partial
// startup; errors are aggregated via errors.Join so a single failing
// phase does not skip the rest.
func (d *Daemon) Shutdown(ctx context.Context) error {
	var errs []error

	if d.tracing != nil {
		if err := d.tracing.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("tracing stop: %w", err))
		}
		// Drain bulk-buffered tracing writes after collectors stop and
		// before BPF teardown — bounded to keep shutdown predictable.
		if err := tracing.CloseStores(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close tracing stores: %w", err))
		}
		d.tracing = nil
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
		log.Infof("huatuo-bamai exited gracefully by syscall.SIGTERM")
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

func (d *Daemon) setupCgroup() error {
	if d.opts.DisableCgroup {
		log.Infof("self cgroup resource limit disabled by --disable-cgroup")
		return nil
	}

	cgr, err := cgroups.NewManager()
	if err != nil {
		return err
	}

	if err := cgr.NewRuntime(
		appName,
		cgroups.ToSpec(
			config.Get().RuntimeCgroup.LimitInitCPU,
			config.Get().RuntimeCgroup.LimitMem,
		),
	); err != nil {
		return fmt.Errorf("new runtime cgroup: %w", err)
	}

	if err := cgr.AddProc(uint64(os.Getpid())); err != nil {
		return fmt.Errorf("cgroup add pid to cgroups.proc")
	}

	d.cgroup = cgr

	return nil
}

func (d *Daemon) applyCgroupCPUQuota() error {
	if d.cgroup == nil {
		return nil
	}
	if err := d.cgroup.UpdateRuntime(cgroups.ToSpec(config.Get().RuntimeCgroup.LimitCPU, 0)); err != nil {
		return fmt.Errorf("update runtime: %w", err)
	}

	return nil
}

func (d *Daemon) setupStorage() error {
	if d.opts.DisableStorage {
		return nil
	}
	return initStorage(d.opts.Region, config.Get())
}

func (d *Daemon) setupBPF() error {
	if err := bpf.NewManager(&bpf.Option{}); err != nil {
		return fmt.Errorf("failed to init bpf manager: %w", err)
	}
	d.bpfReady = true

	return nil
}

func (d *Daemon) setupPodManager() error {
	mgrInitCtx := pod.ManagerInitCtx{
		PodReadOnlyPort:      config.Get().Pod.KubeletReadOnlyPort,
		PodAuthorizedPort:    config.Get().Pod.KubeletAuthorizedPort,
		PodClientCertPath:    config.Get().Pod.KubeletClientCertPath,
		PodContainerDisabled: d.opts.DisableKubelet,
		DockerAPIVersion:     config.Get().Pod.DockerAPIVersion,
	}

	if err := pod.ManagerInit(&mgrInitCtx); err != nil {
		return fmt.Errorf("init podlist and sync module: %w", err)
	}
	d.podReady = true

	return nil
}

func (d *Daemon) setupMetrics() error {
	reg, err := InitMetricsCollector(config.Get().BlackList, d.opts.Region)
	if err != nil {
		return err
	}
	d.metrics = reg

	return nil
}

func (d *Daemon) startToolstream() error {
	srv, err := toolstream.NewServerDefault()
	if err != nil {
		return fmt.Errorf("toolstream: %w", err)
	}

	if err := srv.Start(); err != nil {
		return fmt.Errorf("toolstream: start: %w", err)
	}
	d.tools = srv

	return nil
}

func (d *Daemon) startTracing() error {
	mgr, err := tracing.NewManager(config.Get().BlackList)
	if err != nil {
		return err
	}

	if err := mgr.Start(); err != nil {
		return err
	}
	d.tracing = mgr

	return nil
}

func (d *Daemon) startHandlers() error {
	handlers.Start(config.Get().APIServer.TCPAddr, d.tracing, d.metrics)
	return nil
}

func initStorage(storageRegion string, cfg *config.BamaiConfig) error {
	var (
		err     error
		esStore *storage.Store[*tracing.Document]
	)

	tracingMetadataStores := make([]*storage.Store[*tracing.Document], 0, 2)
	if cfg.Storage.ES.Address != "" &&
		cfg.Storage.ES.Username != "" &&
		cfg.Storage.ES.Password != "" {
		esStore, err = storage.NewFromConfig[*tracing.Document](context.Background(), &driver.Config{
			Driver:      "elasticsearch",
			ESAddresses: splitStorageAddresses(cfg.Storage.ES.Address),
			ESUsername:  cfg.Storage.ES.Username,
			ESPassword:  cfg.Storage.ES.Password,
			ESIndex:     cfg.Storage.ES.Index,
		}, tracing.DocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("storage.NewStore(tracing documents): %w", err)
		}
		tracingMetadataStores = append(tracingMetadataStores, esStore)
	}

	if cfg.Storage.LocalFile.Path != "" {
		localFileStore, err := storage.NewFromConfig[*tracing.Document](context.Background(), &driver.Config{
			Driver:                "localfile",
			LocalFilePath:         cfg.Storage.LocalFile.Path,
			LocalFileMaxRotation:  cfg.Storage.LocalFile.MaxRotation,
			LocalFileRotationSize: cfg.Storage.LocalFile.RotationSize,
		}, tracing.DocumentStoreMapper{})
		if err != nil {
			return fmt.Errorf("storage.NewStore(tracing documents localfile): %w", err)
		}
		tracingMetadataStores = append(tracingMetadataStores, localFileStore)
	}

	if len(tracingMetadataStores) > 0 {
		tracing.SetTracingStore(
			tracingMetadataStores,
			tracing.DocumentOptions{
				Region: storageRegion,
			},
		)
	}
	if esStore != nil {
		tracing.SetTaskStore([]*storage.Store[*tracing.Document]{esStore}, tracing.DocumentOptions{Region: storageRegion})
	}

	return nil
}

func splitStorageAddresses(raw string) []string {
	parts := strings.Split(raw, ",")
	addresses := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		addresses = append(addresses, trimmed)
	}
	return addresses
}
