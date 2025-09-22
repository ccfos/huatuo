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
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/command/container"
	"huatuo-bamai/internal/log"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/perf.c -o perf.o

//go:embed perf.o
var perfBpfObj []byte

func mainAction(ctx *cli.Context) error {
	optBpfObj := ctx.String("bpf-obj")
	optPid := ctx.Uint64("pid")
	optDuration := ctx.Int("duration")

	var targetCssAddr uint64
	if containerID := ctx.String("container-id"); containerID != "" {
		c, err := container.GetContainerByID(ctx.String("server-address"), containerID)
		if err != nil {
			return err
		}
		targetCssAddr = c.CSS["cpu"]
	}

	if err := bpf.InitBpfManager(&bpf.Option{
		KeepaliveTimeout: optDuration,
	}); err != nil {
		return fmt.Errorf("init bpf err %w", err)
	}
	defer bpf.CloseBpfManager()

	b, err := bpf.LoadBpfFromBytes(optBpfObj, perfBpfObj, map[string]any{"css": targetCssAddr, "pid": optPid})
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}
	defer b.Close()

	opt := bpf.AttachOption{
		ProgramName: "perf_event_sw_cpu_clock",
	}
	opt.PerfEvent.SampleFreq = 99
	if err := b.AttachWithOptions([]bpf.AttachOption{opt}); err != nil {
		return fmt.Errorf("attach err %w", err)
	}

	signalWait := make(chan os.Signal, 1)
	signal.Notify(signalWait, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-time.After(time.Duration(optDuration) * time.Second):
	case <-ctx.Done():
		return fmt.Errorf("caller requests stop")
	case sig := <-signalWait:
		return fmt.Errorf("received signal %s", sig)
	}

	if err := parsedata(b); err != nil {
		return fmt.Errorf("parsedata err %w", err)
	}

	return nil
}

func main() {
	app := cli.NewApp()
	app.Usage = "perf"
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "bpf-obj",
			Value: "perf.o",
			Usage: "case name",
		},
		&cli.StringFlag{
			Name:  "container-id",
			Value: "",
			Usage: "Container's ID",
		},
		&cli.Uint64Flag{
			Name:  "pid",
			Value: 0,
			Usage: "Task pid number",
		},
		&cli.IntFlag{
			Name:  "duration",
			Value: 5,
			Usage: "Tool duration(s)",
		},
		&cli.StringFlag{
			Name:  "server-address",
			Value: "127.0.0.1:19704",
			Usage: "huatuo-bamai server address",
		},
	}

	app.Before = func(ctx *cli.Context) error {
		log.SetOutput(io.Discard)
		return nil
	}

	app.Action = mainAction
	if err := app.Run(os.Args); err != nil {
		fmt.Printf("perf: %v\n", err)
		os.Exit(1)
	}
}
