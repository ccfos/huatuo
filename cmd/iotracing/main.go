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
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/toolstream"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iotracing.c -o $BPF_DIR/iotracing.o

const iotracingToolName = "iotracing"

// AppVersion is injected via -ldflags at build time.
var AppVersion = ""

// ioConfig is the validated, typed view of CLI flags consumed by the
// trace pipeline.
type ioConfig struct {
	durationSecond     uint64
	scheduleThreshold  uint64 // ms
	maxFilesPerProcess uint64
	maxProcess         uint64
	maxStack           uint64
}

func main() {
	app := cli.NewApp()
	app.Name = iotracingToolName
	app.Action = mainAction
	app.Flags = appFlags()
	app.Before = func(c *cli.Context) error {
		if err := validateFlags(c); err != nil {
			return err
		}

		// Silence cli library logs so they don't interleave with the
		// tool's own json/text output.
		log.SetOutput(io.Discard)

		return nil
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", iotracingToolName, err)
		os.Exit(1)
	}
}

func mainAction(c *cli.Context) error {
	cfg, filters, err := loadConfig(c)
	if err != nil {
		return err
	}

	client, err := openToolstream(c)
	if err != nil {
		return err
	}

	if client != nil {
		defer client.End()
	}

	if err := bpf.NewManager(&bpf.Option{
		KeepaliveTimeout: int(cfg.durationSecond),
	}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Close()

	result, err := runTrace(c.Context, c.String(cliFlagBpfPath), cfg, filters)
	if err != nil {
		return err
	}

	sink := newWriter(c.String(cliFlagOutput), client)
	if err := sink.Write(result); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

// openToolstream returns a connected toolstream client when
// --output-storage is set, or nil otherwise. The caller owns End().
func openToolstream(c *cli.Context) (*toolstream.Client, error) {
	path := c.String(cliFlagOutputStorage)
	if path == "" {
		return nil, nil
	}

	client, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: path,
		ToolName: iotracingToolName,
		Version:  AppVersion,
		TaskID:   c.String(cliFlagTaskID),
	})
	if err != nil {
		return nil, fmt.Errorf("--output-storage: %w", err)
	}

	return client, nil
}
