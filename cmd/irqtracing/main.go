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
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/version"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/irqtracing.c -o $BPF_DIR/irqtracing.o

const (
	irqTracingToolName      = "irqtracing"
	sourceRateLimitConstant = "source_rate_limit"
	victimRateLimitConstant = "victim_rate_limit"
)

// Set by Makefile via -ldflags -X. Must live in package main; an empty
// value falls back to version.Devel via version.Resolve.
var (
	AppVersion   string
	AppGitCommit string
	AppBuildTime string
	versionInfo  version.Info
)

// IRQTracingResult is the payload emitted by the CLI. A non-zero NMissed means
// the flame graph is incomplete.
type IRQTracingResult struct {
	FlameData *profiler.ProfileData `json:"flamedata"`
	NMissed   uint64                `json:"nmissed"`
}

func mainAction(cliCtx *cli.Context) (returnErr error) {
	cfg, err := loadConfig(cliCtx)
	if err != nil {
		return err
	}

	client, err := openToolstream(cliCtx)
	if err != nil {
		return err
	}
	if client != nil {
		defer func() {
			if err := client.End(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close toolstream: %w", err))
			}
		}()
	}

	if err := bpf.Init(&bpf.Option{
		KeepaliveTimeout: cfg.duration,
	}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Shutdown()

	bpfBytes, err := os.ReadFile(cfg.bpfPath)
	if err != nil {
		return fmt.Errorf("read bpf object: %w", err)
	}

	b, err := bpf.LoadBPFFromBytes(
		fmt.Sprintf("irqtracing_%d.o", time.Now().UnixNano()),
		bpfBytes,
		irqTracingBPFConstants(cfg.targetCPU, cfg.maxEventsPerSecondPerCPU),
	)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	ctx, stop := signal.NotifyContext(
		cliCtx.Context,
		syscall.SIGHUP,
		syscall.SIGQUIT,
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer stop()

	profileStartedAt := time.Now()
	if err := b.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "probe_softirq_raise", Symbol: "irq/softirq_raise"},
		{ProgramName: "probe_softirq_entry", Symbol: "irq/softirq_entry"},
	}); err != nil {
		return fmt.Errorf("attach: %w", err)
	}

	timer := time.NewTimer(time.Duration(cfg.duration) * time.Second)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-ctx.Done():
		return fmt.Errorf("collection stopped: %w", ctx.Err())
	}

	// Freeze the collection point before reading the drop counter.
	nmissed, err := detachAndReadDroppedSamples(b)
	if err != nil {
		return err
	}
	if nmissed > 0 {
		writeDroppedSamplesWarning(cliCtx.App.ErrWriter, nmissed)
	}

	flameData, stacks, err := buildFlameGraph(b, profileStartedAt)
	if err != nil {
		return fmt.Errorf("build flamegraph: %w", err)
	}

	result := &IRQTracingResult{
		FlameData: flameData,
		NMissed:   nmissed,
	}
	snapshot := &irqTracingSnapshot{result: result, stacks: stacks}
	outputWriter, err := newWriter(cliCtx.App.Writer, cfg.output, client)
	if err != nil {
		return fmt.Errorf("create output writer: %w", err)
	}
	if err := outputWriter.Write(snapshot); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// irqTracingBPFConstants divides each CPU's budget between the source and
// victim probes. An odd remainder goes to source.
func irqTracingBPFConstants(targetCPU int32, maxEventsPerSecondPerCPU uint64) map[string]any {
	constants := map[string]any{"target_cpu": targetCPU}
	if maxEventsPerSecondPerCPU == 0 {
		return constants
	}

	sourceRate := maxEventsPerSecondPerCPU/2 + maxEventsPerSecondPerCPU%2
	victimRate := maxEventsPerSecondPerCPU / 2
	constants[sourceRateLimitConstant] = uint32(sourceRate)
	constants[victimRateLimitConstant] = uint32(victimRate)
	return constants
}

func openToolstream(cliCtx *cli.Context) (*toolstream.Client, error) {
	sockPath := cliCtx.String(cliFlagOutputStorage)
	if sockPath == "" {
		return nil, nil
	}

	client, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: sockPath,
		ToolName: irqTracingToolName,
		Version:  versionInfo.Version,
		TaskID:   cliCtx.String(cliFlagTaskID),
	})
	if err != nil {
		return nil, fmt.Errorf("--output-storage: %w", err)
	}

	return client, nil
}

func writeDroppedSamplesWarning(output io.Writer, nmissed uint64) {
	if output == nil {
		output = os.Stderr
	}
	fmt.Fprintf(output,
		"irqtracing: %d samples dropped; the flame graph is incomplete\n", nmissed)
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
		log.SetOutput(io.Discard)
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
