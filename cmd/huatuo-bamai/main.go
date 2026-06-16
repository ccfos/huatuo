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
)

const (
	appName  = "huatuo-bamai"
	appUsage = "An In-depth Observation of Linux Kernel Application"
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
	if err := pidfile.Lock(appName); err != nil {
		return fmt.Errorf("failed to lock pid file: %w", err)
	}
	defer pidfile.UnLock(appName)

	// init cpu quota; nil cgr means --disable-cgroup is set
	var cgr cgroups.Cgroup
	if opts.DisableCgroup {
		log.Infof("self cgroup resource limit disabled by --disable-cgroup")
	} else {
		var err error
		cgr, err = cgroups.NewManager()
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
		defer func() {
			_ = cgr.DeleteRuntime()
		}()

		if err := cgr.AddProc(uint64(os.Getpid())); err != nil {
			return fmt.Errorf("cgroup add pid to cgroups.proc")
		}
	}

	if !opts.DisableStorage {
		if err := initStorage(opts.Region, config.Get()); err != nil {
			return err
		}
	}

	if err := bpf.NewManager(&bpf.Option{}); err != nil {
		return fmt.Errorf("failed to init bpf manager: %w", err)
	}

	mgrInitCtx := pod.ManagerInitCtx{
		PodReadOnlyPort:      config.Get().Pod.KubeletReadOnlyPort,
		PodAuthorizedPort:    config.Get().Pod.KubeletAuthorizedPort,
		PodClientCertPath:    config.Get().Pod.KubeletClientCertPath,
		PodContainerDisabled: opts.DisableKubelet,
		DockerAPIVersion:     config.Get().Pod.DockerAPIVersion,
	}

	if err := pod.ManagerInit(&mgrInitCtx); err != nil {
		return fmt.Errorf("init podlist and sync module: %w", err)
	}

	blacklisted := config.Get().BlackList
	prom, err := InitMetricsCollector(blacklisted, opts.Region)
	if err != nil {
		return err
	}

	tsSrv, err := toolstream.NewServerDefault()
	if err != nil {
		return fmt.Errorf("toolstream: %w", err)
	}
	if err := tsSrv.Start(); err != nil {
		return fmt.Errorf("toolstream: start: %w", err)
	}
	defer tsSrv.Close()

	mgr, err := tracing.NewManager(blacklisted)
	if err != nil {
		return err
	}

	if err := mgr.Start(); err != nil {
		return err
	}

	handlers.Start(config.Get().APIServer.TCPAddr, mgr, prom)

	// update cpu quota; skipped when --disable-cgroup is set
	if cgr != nil {
		if err := cgr.UpdateRuntime(cgroups.ToSpec(config.Get().RuntimeCgroup.LimitCPU, 0)); err != nil {
			return fmt.Errorf("update runtime: %w", err)
		}
	}

	waitExit := make(chan os.Signal, 1)
	signal.Notify(waitExit, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM)

	if opts.DryRun {
		time.Sleep(2 * time.Second)
		log.Infof("huatuo-bamai exited gracefully by syscall.SIGTERM")
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	}

	log.Infof("huatuo-bamai now starting success")

	for {
		s := <-waitExit
		switch s {
		case syscall.SIGQUIT, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM:
			log.Infof("huatuo-bamai exited by signal %d", s)
			_ = mgr.Stop()
			// Drain bulk-buffered tracing writes after collectors stop and
			// before BPF teardown — bounded to keep shutdown predictable.
			closeCtx, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
			if err := tracing.CloseStores(closeCtx); err != nil {
				log.Warnf("close tracing stores: %v", err)
			}
			cancelClose()
			bpf.Close()
			pod.ManagerRelease()
			return nil
		case syscall.SIGUSR1:
			return nil
		default:
			return nil
		}
	}
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
