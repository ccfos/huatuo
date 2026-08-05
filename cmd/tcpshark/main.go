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

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/tcp_retransmit.c -o $BPF_DIR/tcp_retransmit.o

const tcpSharkToolName = "tcpshark"

var (
	AppVersion   string
	AppGitCommit string
	AppBuildTime string
)

func main() {
	log.SetOutput(os.Stderr)

	app := &cli.App{
		Name:      tcpSharkToolName,
		Usage:     "trace TCP events",
		Flags:     appFlags(),
		Before:    validateFlags,
		Writer:    os.Stdout,
		ErrWriter: os.Stderr,
	}
	versionInfo := version.Wire(app, version.Seed{
		Name:      tcpSharkToolName,
		Version:   AppVersion,
		GitCommit: AppGitCommit,
		BuildTime: AppBuildTime,
	})
	app.Action = func(c *cli.Context) error {
		return runRetransmit(c.Context, &retransmitOptions{
			bpfPath:            c.String(cliFlagBpfPath),
			filterExpression:   c.String(cliFlagFilter),
			durationSeconds:    c.Int(cliFlagDuration),
			outputFormat:       c.String(cliFlagOutput),
			outputStorage:      c.String(cliFlagOutputStorage),
			taskID:             c.String(cliFlagTaskID),
			sourceType:         c.String(cliFlagSourceTypes),
			maxEventsPerSecond: c.Uint64(cliFlagMaxEventsPerSecond),
			isTLPEnabled:       c.Bool(cliFlagEnableTLP),
			version:            versionInfo.Version,
			output:             c.App.Writer,
		})
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		unix.SIGINT,
		unix.SIGTERM,
	)
	defer stop()

	if err := app.RunContext(ctx, os.Args); err != nil {
		log.WithError(err).Error("run tcpshark")
		os.Exit(1)
	}
}
