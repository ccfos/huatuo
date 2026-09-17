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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/version"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/irq_tracing.c -o $BPF_DIR/irq_tracing.o

const irqTracingToolName = "irqtracing"

// Set by Makefile via -ldflags -X. Must live in package main; an empty
// value falls back to version.Devel via version.Resolve.
var (
	AppVersion   string
	AppGitCommit string
	AppBuildTime string
)

// IrqTracingResult is the JSON payload written by the CLI. autotracing reads it
// and merges the flame graph and runqueue victims into the saved tracing data.
// A non-zero NMissed means samples were dropped during collection (sample
// budget or full counts maps) and the flame graph is incomplete.
type IrqTracingResult struct {
	FlameData *profiler.ProfileData `json:"flamedata"`
	RqTasks   []string              `json:"rq_tasks"`
	NMissed   uint64                `json:"nmissed"`
}

func mainAction(cliCtx *cli.Context) error {
	targetCPU := cliCtx.Uint64("target-cpu")
	duration := cliCtx.Int("duration")
	outputPath := cliCtx.String("output-path")
	bpfPath := cliCtx.String("bpf-path")

	if err := bpf.Init(&bpf.Option{
		KeepaliveTimeout: duration,
	}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Shutdown()

	bpfBytes, err := os.ReadFile(bpfPath)
	if err != nil {
		return fmt.Errorf("read bpf object: %w", err)
	}

	b, err := bpf.LoadBPFFromBytes(
		fmt.Sprintf("irqtracing_%d.o", time.Now().UnixNano()),
		bpfBytes,
		map[string]any{
			"target_cpu": uint32(targetCPU),
		},
	)
	if err != nil {
		return fmt.Errorf("load bpf: %w", err)
	}
	defer b.Close()

	ctx, cancel := context.WithCancel(cliCtx.Context)
	defer cancel()

	if err := b.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "probe_activate_task", Symbol: "activate_task"},
		{ProgramName: "probe_deactivate_task", Symbol: "deactivate_task"},
		{ProgramName: "probe_softirq_raise", Symbol: "irq/softirq_raise"},
		{ProgramName: "probe_softirq_entry", Symbol: "irq/softirq_entry"},
	}); err != nil {
		return fmt.Errorf("attach: %w", err)
	}

	signalWait := make(chan os.Signal, 1)
	signal.Notify(signalWait, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-time.After(time.Duration(duration) * time.Second):
	case <-ctx.Done():
		return fmt.Errorf("caller requests stop")
	case sig := <-signalWait:
		return fmt.Errorf("received signal %s", sig)
	}

	// Freeze the collection point and read every post-window state map: the
	// probes are detached before any read so the drop counter, the
	// ksoftirqd window, the runqueue dump and the flame graph all describe
	// the same cut-off.
	rqTasks, ksoftirqdHit, ksoftirqdVec, nmissed, err := readPostWindowSnapshot(b)
	if err != nil {
		return err
	}
	if nmissed > 0 {
		fmt.Fprintf(os.Stderr,
			"irqtracing: %d samples dropped; the flame graph is incomplete\n", nmissed)
	}

	// When ksoftirqd ran the softirq, the tasks queued on the runqueue were the
	// victims waiting behind it; include them in the flame graph as victims.
	var rqVictims []rqVictim
	if ksoftirqdHit {
		rqVictims = rqTasks
	}

	flameData, err := buildFlameGraph(b, rqVictims, ksoftirqdVec)
	if err != nil {
		return fmt.Errorf("build flamegraph: %w", err)
	}

	rqTaskStrs := make([]string, 0, len(rqTasks))
	for _, t := range rqTasks {
		rqTaskStrs = append(rqTaskStrs, fmt.Sprintf("%s(%d)", t.comm, t.pid))
	}

	result := IrqTracingResult{
		FlameData: flameData,
		RqTasks:   rqTaskStrs,
		NMissed:   nmissed,
	}

	return writeResult(outputPath, &result)
}

// writeResult marshals result to a JSON file under outputPath and prints the
// file path to stdout so the caller can locate it.
func writeResult(outputPath string, result *IrqTracingResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	file := filepath.Join(outputPath, fmt.Sprintf("irqtracing_%d.json", time.Now().UnixNano()))
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return fmt.Errorf("write result: %w", err)
	}

	fmt.Println(file)
	return nil
}

func main() {
	app := cli.NewApp()
	app.Name = irqTracingToolName
	app.Usage = "collect irq/softirq source and victim stacks of a target cpu and build a flame graph"
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:     "bpf-path",
			Usage:    "path to the irqtracing BPF object file",
			Required: true,
		},
		&cli.Uint64Flag{
			Name:     "target-cpu",
			Usage:    "the cpu to trace",
			Required: true,
		},
		&cli.IntFlag{
			Name:  "duration",
			Value: 3,
			Usage: "collect duration in seconds",
		},
		&cli.StringFlag{
			Name:  "output-path",
			Value: ".",
			Usage: "directory to write the result JSON file",
		},
	}

	app.Before = func(ctx *cli.Context) error {
		log.SetOutput(io.Discard)
		return nil
	}

	version.Wire(app, version.Seed{
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
