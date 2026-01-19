// Copyright 2025 The HuaTuo Authors
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
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	_ "huatuo-bamai/core/autotracing"
	_ "huatuo-bamai/core/events"
	_ "huatuo-bamai/core/metrics"
	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/conf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pidfile"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/services"
	"huatuo-bamai/internal/storage"
	"huatuo-bamai/internal/utils/executil"
	"huatuo-bamai/pkg/tracing"

	"github.com/urfave/cli/v2"
)

func mainAction(ctx *cli.Context) error {
	if ctx.NArg() > 0 {
		return fmt.Errorf("invalid param %v", ctx.Args())
	}

	if err := pidfile.Lock(ctx.App.Name); err != nil {
		return fmt.Errorf("failed to lock pid file: %w", err)
	}
	defer pidfile.Remove(ctx.App.Name)

	// init cpu quota
	cgr, err := cgroups.NewCgroupManager()
	if err != nil {
		return err
	}

	if err := cgr.NewRuntime(ctx.App.Name,
		cgroups.ToSpec(
			conf.Get().RuntimeCgroup.LimitInitCPU,
			conf.Get().RuntimeCgroup.LimitMem,
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

	// initialize the storage clients.
	storageInitCtx := storage.InitContext{
		EsAddresses:       conf.Get().Storage.ES.Address,
		EsUsername:        conf.Get().Storage.ES.Username,
		EsPassword:        conf.Get().Storage.ES.Password,
		EsIndex:           conf.Get().Storage.ES.Index,
		LocalPath:         conf.Get().Storage.LocalFile.Path,
		LocalMaxRotation:  conf.Get().Storage.LocalFile.MaxRotation,
		LocalRotationSize: conf.Get().Storage.LocalFile.RotationSize,
		StorageDisabled:   ctx.Bool("disable-storage"),
		Region:            conf.Region,
	}

	if err := storage.InitDefaultClients(&storageInitCtx); err != nil {
		return fmt.Errorf("storage.InitDefaultClients: %w", err)
	}

	if err := bpf.InitBpfManager(&bpf.Option{}); err != nil {
		return fmt.Errorf("failed to init bpf manager: %w", err)
	}

	podListInitCtx := pod.PodContainerInitCtx{
		PodReadOnlyPort:      conf.Get().Pod.KubeletReadOnlyPort,
		PodAuthorizedPort:    conf.Get().Pod.KubeletAuthorizedPort,
		PodClientCertPath:    conf.Get().Pod.KubeletClientCertPath,
		PodContainerDisabled: ctx.Bool("disable-kubelet"),
	}

	if err := pod.ContainerPodMgrInit(&podListInitCtx); err != nil {
		return fmt.Errorf("init podlist and sync module: %w", err)
	}

	blacklisted := conf.Get().BlackList
	prom, err := InitMetricsCollector(blacklisted, conf.Region)
	if err != nil {
		return fmt.Errorf("InitMetricsCollector: %w", err)
	}

	mgr, err := tracing.NewMgrTracingEvent(blacklisted)
	if err != nil {
		return err
	}

	if err := mgr.MgrTracingEventStartAll(); err != nil {
		return err
	}

	log.Infof("Initialize the Metrics collector: %v", prom)
	services.Start(conf.Get().APIServer.TCPAddr, mgr, prom)

	// update cpu quota
	if err := cgr.UpdateRuntime(cgroups.ToSpec(conf.Get().RuntimeCgroup.LimitCPU, 0)); err != nil {
		return fmt.Errorf("update runtime: %w", err)
	}

	waitExit := make(chan os.Signal, 1)
	signal.Notify(waitExit, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM)

	if ctx.Bool("dry-run") {
		time.Sleep(2 * time.Second)
		log.Infof("huatuo-bamai exit gracefully by syscall.SIGTERM")
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	}

	for {
		s := <-waitExit
		switch s {
		case syscall.SIGQUIT, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM:
			log.Infof("huatuo-bamai exit by signal %d", s)
			_ = mgr.MgrTracingEventStopAll()
			bpf.CloseBpfManager()
			pod.ContainerPodMgrClose()
			return nil
		case syscall.SIGUSR1:
			return nil
		default:
			return nil
		}
	}
}

var (
	// AppGitCommit will be the hash that the binary was built from
	// and will be populated by the Makefile
	AppGitCommit string
	// AppBuildTime will be populated by the Makefile
	AppBuildTime string
	// AppVersion will be populated by the Makefile, read from
	// VERSION file of the source code.
	AppVersion string
	AppUsage   = "An In-depth Observation of Linux Kernel Application"
)

const (
	optionBpfObjDir  = "bpf-dir"
	optionToolBinDir = "tools-bin-dir"
	optionConfigDir  = "config-dir"
)

func buildOptionDir(optionDir string, ctx *cli.Context) string {
	dir := ctx.String(optionDir)
	if filepath.IsAbs(dir) {
		return dir
	}

	if ctx.IsSet(optionDir) {
		return dir
	}

	runningDir, err := executil.RunningDir()
	if err != nil {
		panic("find running dir")
	}

	return filepath.Join(runningDir, "../", dir)
}

func main() {
	app := cli.NewApp()
	app.Usage = AppUsage

	if AppVersion == "" {
		panic("the value of AppVersion must be specified")
	}

	v := []string{
		"",
		fmt.Sprintf("   app_version: %s", AppVersion),
		fmt.Sprintf("   go_version: %s", runtime.Version()),
		fmt.Sprintf("   git_commit: %s", AppGitCommit),
		fmt.Sprintf("   build_time: %s", AppBuildTime),
	}
	app.Version = strings.Join(v, "\n")

	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "config",
			Value: "huatuo-bamai.conf",
			Usage: "huatuo-bamai config file",
		},
		&cli.StringFlag{
			Name:  optionConfigDir,
			Value: "conf",
			Usage: "huatuo config dir",
		},
		&cli.StringFlag{
			Name:  optionBpfObjDir,
			Value: "bpf",
			Usage: "bpf obj dir",
		},
		&cli.StringFlag{
			Name:  optionToolBinDir,
			Value: "bin",
			Usage: "tools bin dir",
		},
		&cli.StringFlag{
			Name:     "region",
			Required: true,
			Usage:    "the host and containers are in this region",
		},
		&cli.BoolFlag{
			Name:  "disable-kubelet",
			Value: false,
			Usage: "disable kubelet(testing only). Not recommended for production use.",
		},
		&cli.BoolFlag{
			Name:  "disable-storage",
			Value: false,
			Usage: "disable storage backends(testing only). Not recommended for production use.",
		},
		&cli.StringSliceFlag{
			Name:  "disable-tracing",
			Usage: "disable tracing. This is related to Blacklist in config, and complement each other",
		},
		&cli.BoolFlag{
			Name:  "log-debug",
			Usage: "enable debug output for logging",
		},
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "for loading tests, exit gracefully",
		},
		&cli.StringFlag{
			Name:  "procfs",
			Usage: "procfs mountpoint",
		},
		&cli.StringFlag{
			Name:  "sysfs",
			Usage: "sysfs mountpoint",
		},
	}

	app.Before = func(ctx *cli.Context) error {
		bpf.DefaultBpfObjDir = buildOptionDir(optionBpfObjDir, ctx)
		tracing.TaskBinDir = buildOptionDir(optionToolBinDir, ctx)

		configDir := buildOptionDir(optionConfigDir, ctx)
		if err := conf.LoadConfig(filepath.Join(configDir, ctx.String("config"))); err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		// set Region
		conf.Region = ctx.String("region")

		// log level
		if conf.Get().Log.Level != "" {
			log.SetLevel(conf.Get().Log.Level)
			log.Infof("log level [%s] configured in file, use it", log.GetLevel())
		}

		logFile := conf.Get().Log.File
		if logFile != "" {
			file, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
			if err == nil {
				log.SetOutput(file)
			} else {
				log.SetOutput(os.Stdout)
				log.Infof("Failed to log to file, using default stdout")
			}
		}

		// tracer
		disabledTracing := ctx.StringSlice("disable-tracing")
		if len(disabledTracing) > 0 {
			definedTracers := conf.Get().BlackList
			definedTracers = append(definedTracers, disabledTracing...)

			conf.Set("Blacklist", definedTracers)
			log.Infof("The tracer black list by cli: %v", conf.Get().BlackList)
		}

		// mountpoint (test only)
		if ctx.String("procfs") != "" {
			procfs.DefaultProcMountPoint = ctx.String("procfs")
		}
		if ctx.String("sysfs") != "" {
			procfs.DefaultSysMountPoint = ctx.String("sysfs")
		}

		if ctx.Bool("log-debug") {
			log.SetLevel("Debug")
		}

		// print dirs
		log.Debugf("option %s: %s, %s: %s, %s: %s", optionBpfObjDir, bpf.DefaultBpfObjDir,
			optionToolBinDir, tracing.TaskBinDir, optionConfigDir, configDir)

		return nil
	}

	// core
	app.Action = mainAction

	// run
	if err := app.Run(os.Args); err != nil {
		log.Errorf("Error: %v", err)
		os.Exit(1)
	}
}
