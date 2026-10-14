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
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ccfos/huatuo/internal/profiler/aggregator"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	profileroutput "github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/profiler/registry"
	"github.com/ccfos/huatuo/internal/version"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/irqtracing.c -o $BPF_DIR/irqtracing.o

const irqTracingToolName = "irqtracing"

// Set by Makefile via -ldflags -X. Must live in package main; an empty
// value falls back to version.Devel via version.Resolve.
var (
	AppVersion   string
	AppGitCommit string
	AppBuildTime string
	versionInfo  version.Info
)

func mainAction(cliCtx *cli.Context) error {
	cfg, err := loadConfig(cliCtx)
	if err != nil {
		return err
	}
	if err := bpf.Init(&bpf.Option{KeepaliveTimeout: cfg.duration}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Shutdown()

	ctx, cancel := signal.NotifyContext(cliCtx.Context, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	format, destination := cfg.output, "-"
	if path := cliCtx.String(cliFlagOutputStorage); path != "" {
		format, destination = profileroutput.FormatRemoteStore, path
	}
	pctx := &pcontext.ProfilerContext{
		Ctx: ctx, Cancel: cancel, Duration: cfg.duration, AggregationMode: aggregator.AggregationModeOneShot,
		OutputFormat: format, OutputPath: destination,
		ToolName: irqTracingToolName, Version: versionInfo.Version,
		TracerID: cliCtx.String(cliFlagTaskID),
	}

	return registry.Profile(pctx, newIRQProfiler(&collector{
		bpfPath:                  cfg.bpfPath,
		cpuId:                    cfg.targetCPU,
		duration:                 time.Duration(cfg.duration) * time.Second,
		maxEventsPerSecondPerCPU: cfg.maxEventsPerSecondPerCPU,
	}))
}

func main() {
	app := cli.NewApp()
	app.Name = irqTracingToolName
	app.Usage = "collect and export irq/softirq source and victim stack profiles"
	app.Flags = appFlags()

	app.Before = func(ctx *cli.Context) error {
		if err := validateFlags(ctx); err != nil {
			return err
		}
		log.SetOutput(ctx.App.ErrWriter)
		log.SetLevel("warn")
		return nil
	}

	versionInfo = version.Wire(app, version.Seed{
		Name:      irqTracingToolName,
		Version:   AppVersion,
		GitCommit: AppGitCommit,
		BuildTime: AppBuildTime,
	})

	app.Action = mainAction
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "irqtracing:", err)
		os.Exit(1)
	}
}
