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
	"os"
	"os/signal"

	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/version"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/dropwatch.c -o $BPF_DIR/dropwatch.o

var (
	dropwatchToolName = "dropwatch"

	AppVersion   string
	AppGitCommit string
	AppBuildTime string
)

func main() {
	app := &cli.App{
		Name:      dropwatchToolName,
		Usage:     "eBPF tracer for Linux kernel packet drops",
		Flags:     appFlags(),
		Before:    validateFlags,
		Writer:    os.Stdout,
		ErrWriter: os.Stderr,
	}
	versionInfo := version.Wire(app, version.Seed{
		Name:      dropwatchToolName,
		Version:   AppVersion,
		GitCommit: AppGitCommit,
		BuildTime: AppBuildTime,
	})
	app.Action = func(c *cli.Context) error {
		return mainAction(c.Context, &dropwatchOptions{
			bpfPath:            c.String(cliFlagBpfPath),
			filterExpression:   c.String(cliFlagFilter),
			device:             c.String(cliFlagDevice),
			deviceExcluded:     c.String(cliFlagDeviceExcluded),
			durationSeconds:    c.Int(cliFlagDuration),
			outputFormat:       c.String(cliFlagOutput),
			outputStorage:      c.String(cliFlagOutputStorage),
			taskID:             c.String(cliFlagTaskID),
			maxEventsPerSecond: c.Uint64(cliFlagMaxEventsPerSecond),
			sourceType:         c.String(cliFlagSourceTypes),
			version:            versionInfo.Version,
			output:             c.App.Writer,
		})
	}

	ctx, stop := signal.NotifyContext(context.Background(), unix.SIGINT, unix.SIGTERM)
	defer stop()

	log.SetOutput(os.Stderr)
	if err := app.RunContext(ctx, os.Args); err != nil {
		log.WithError(err).Error("run dropwatch")
		os.Exit(1)
	}
}
