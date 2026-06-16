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
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers"
	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/pidfile"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/utils/executil"

	"github.com/urfave/cli/v2"
)

const (
	huatuoAPIServerUsage = "Control-plane API server for orchestrating HuaTuo profiling and tracing jobs across hosts"
	optionConfigDir      = "config-dir"
)

var (
	// AppGitCommit will be the hash that the binary was built from
	// and will be populated by the Makefile
	AppGitCommit string
	// AppBuildTime will be populated by the Makefile
	AppBuildTime string
	// AppVersion will be populated by the Makefile, read from
	// VERSION file of the source code.
	AppVersion string
)

type fatalWriter struct {
	cliErrWriter io.Writer
}

func (f *fatalWriter) Write(p []byte) (n int, err error) {
	log.Errorf("%s", p)
	return f.cliErrWriter.Write(p)
}

func buildOptionDir(dir string) (string, error) {
	if filepath.IsAbs(dir) {
		return dir, nil
	}

	runningDir, err := executil.RunningDir()
	if err != nil {
		return "", fmt.Errorf("failed to find running directory: %w", err)
	}

	return filepath.Join(runningDir, "../", dir), nil
}

func main() {
	app := cli.NewApp()
	app.Name = "huatuo-apiserver"
	app.Usage = huatuoAPIServerUsage

	if AppVersion == "" {
		log.Error("the value of AppVersion must be specified")
		os.Exit(1)
	}
	var v []string
	v = append(v, fmt.Sprintf("App version: %s", AppVersion))
	if AppGitCommit != "" {
		v = append(v, fmt.Sprintf("   commit: %s", AppGitCommit))
	}
	if AppBuildTime != "" {
		v = append(v, fmt.Sprintf("   build time: %s", AppBuildTime))
	}
	app.Version = strings.Join(v, "\n")

	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "config",
			Value: "huatuo-apiserver.conf",
			Usage: "huatuo-apiserver config file",
		},
		&cli.StringFlag{
			Name:  optionConfigDir,
			Value: "conf",
			Usage: "huatuo config dir",
		},
		&cli.BoolFlag{
			Name:  "enable-pprof",
			Usage: "package pprof serves via its HTTP server runtime profiling data, default(false)",
		},
	}

	app.Before = func(ctx *cli.Context) error {
		configDir, err := buildOptionDir(ctx.String(optionConfigDir))
		if err != nil {
			return err
		}
		if err := config.Load(filepath.Join(configDir, ctx.String("config"))); err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		if config.Get().LogLevel != "" {
			log.SetLevel(config.Get().LogLevel)
			log.Infof("log level [%s] configured in file, use it", log.GetLevel())
		}

		/* pprof */
		if ctx.Bool("enable-pprof") {
			go func() {
				log.Infof("pprof server started on [::]:6062")
				server := &http.Server{
					Addr:              ":6062",
					ReadHeaderTimeout: 30 * time.Second,
				}
				log.Error(server.ListenAndServe())
			}()
		}

		return nil
	}

	app.Action = mainAction

	// If the command returns an error, cli takes upon itself to print
	// the error on cli.ErrWriter and exit.
	// Use our own writer here to ensure the log gets sent to the right location.
	cli.ErrWriter = &fatalWriter{cli.ErrWriter}

	if err := app.Run(os.Args); err != nil {
		log.Errorf("Error: %v", err)
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}

func mainAction(ctx *cli.Context) error {
	if ctx.NArg() > 0 {
		return fmt.Errorf("invalid param %v", ctx.Args())
	}

	lk, err := pidfile.Lock("huatuo-apiserver")
	if err != nil {
		return fmt.Errorf("failed to lock pid file: %w", err)
	}
	defer lk.Unlock()

	// init cpu quota
	cgr, err := cgroups.NewManager()
	if err != nil {
		return err
	}

	if err := cgr.NewRuntime(
		ctx.App.Name,
		cgroups.ToSpec(
			float64(config.Get().RuntimeCgroup.LimitCPU),
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

	nodeAgent := job.NewHTTPNodeAgent()

	// Create separate managers for profiling and tracing
	profilingManager, err := job.NewManager(context.Background(), nodeAgent, job.ManagerConfig{
		MaxJobsPerHost: config.Get().TaskConfig.MaxProfilingTasksPerHost,
		MaxTotalJobs:   config.Get().TaskConfig.MaxTotalProfilingTasks,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize profiling manager: %w", err)
	}

	tracingManager, err := job.NewManager(context.Background(), nodeAgent, job.ManagerConfig{
		MaxJobsPerHost: config.Get().TaskConfig.MaxTracingTasksPerHost,
		MaxTotalJobs:   config.Get().TaskConfig.MaxTotalTracingTasks,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize tracing manager: %w", err)
	}

	// profiling flamegraph
	esConfig := &profileService.ElasticSearchConfig{
		Address:  config.Get().ElasticSearch.Address,
		Username: config.Get().ElasticSearch.Username,
		Password: config.Get().ElasticSearch.Password,
		Index:    config.Get().ElasticSearch.Index,
	}
	if err := profileService.InitializeProfileFlamegraph(esConfig); err != nil {
		return fmt.Errorf("initialize profiling flamegraph: %w", err)
	}

	promRegistry, err := InitMetricsCollector()
	if err != nil {
		return fmt.Errorf("initialize metrics collector: %w", err)
	}

	if err := handlers.ServerStart(config.Get().APIServer.TCPAddr, promRegistry, profilingManager, tracingManager); err != nil {
		return fmt.Errorf("handlers.APIServer: %w", err)
	}

	waitExit := make(chan os.Signal, 1)
	signal.Notify(waitExit, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM)
	s := <-waitExit
	log.Infof("huatuo-apiserver exit by signal %d", s)
	return nil
}
