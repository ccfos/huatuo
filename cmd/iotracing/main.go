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
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/internal/bpf"
	serverpprof "github.com/ccfos/huatuo/internal/httpserver/pprof"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/internal/version"
	"github.com/ccfos/huatuo/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/iotracing.c -o $BPF_DIR/iotracing.o

const iotracingToolName = "iotracing"

// Set by Makefile via -ldflags -X. Must live in package main; an empty
// value falls back to version.Devel via version.Resolve.
var (
	AppVersion   string
	AppGitCommit string
	AppBuildTime string

	// versionInfo is the resolved build identity, populated in main and
	// consumed by mainAction (toolstream).
	versionInfo version.Info
)

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
	app.Usage = "eBPF tracer for Linux block I/O latency, with per-process and per-file attribution"
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

	versionInfo = version.Wire(app, version.Seed{
		Name:      iotracingToolName,
		Version:   AppVersion,
		GitCommit: AppGitCommit,
		BuildTime: AppBuildTime,
	})

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", iotracingToolName, err)
		os.Exit(1)
	}
}

func mainAction(c *cli.Context) (returnErr error) {
	cfg, filters, err := loadConfig(c)
	if err != nil {
		return err
	}

	if c.Bool(cliFlagEnablePProf) {
		server, err := serverpprof.StartDefault(c.Context)
		if err != nil {
			return err
		}
		defer func() {
			if err := server.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close pprof server: %w", err))
			}
		}()
	}

	blockDevice, err := requestPartIsBlockDevice()
	if err != nil {
		return err
	}
	filters[bpfRequestPartBlockDevice] = blockDevice

	client, err := openToolstream(c)
	if err != nil {
		return err
	}

	if client != nil {
		// The final report writer closes this connection; this fallback also
		// covers setup failures before ownership reaches that writer.
		defer client.Close()
	}

	if err := bpf.Init(&bpf.Option{
		KeepaliveTimeout: int(cfg.durationSecond),
	}); err != nil {
		return fmt.Errorf("init bpf: %w", err)
	}
	defer bpf.Shutdown()

	result, err := runTrace(c.Context, c.String(cliFlagBpfPath), cfg, filters)
	if client != nil {
		return writeToolstreamReport(
			c.Context,
			client,
			result,
			err,
		)
	}

	return writeReport(newWriter(c.String(cliFlagOutput), nil), result, err)
}

func writeToolstreamReport(
	ctx context.Context,
	client *toolstream.Client,
	result *types.IOTracingSnapshot,
	traceErr error,
) (returnErr error) {
	// A stopped collection still publishes its final snapshot, but neither a
	// blocked report nor its End frame may hold shutdown indefinitely.
	events := toolstream.NewFinalEventWriter(context.WithoutCancel(ctx), client)
	defer func() {
		if err := events.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close toolstream: %w", err))
		}
	}()

	return writeReport(newWriter("", events), result, traceErr)
}

// Publish captured observations before returning a collection failure.
func writeReport(sink writer, result *types.IOTracingSnapshot, traceErr error) error {
	if result == nil {
		return traceErr
	}

	writeErr := sink.Write(result)
	if writeErr != nil {
		writeErr = fmt.Errorf("write output: %w", writeErr)
	}

	return errors.Join(traceErr, writeErr)
}

// openToolstream returns a connected client when --output-storage is set.
// The caller owns the connection until it transfers it to the final report writer.
func openToolstream(c *cli.Context) (*toolstream.Client, error) {
	path := c.String(cliFlagOutputStorage)
	if path == "" {
		return nil, nil
	}

	client, err := toolstream.NewClient(&toolstream.ClientOptions{
		SockPath:    path,
		ServiceName: iotracingToolName,
		Version:     versionInfo.Version,
		TaskID:      c.String(cliFlagTaskID),
	})
	if err != nil {
		return nil, fmt.Errorf("--output-storage: %w", err)
	}

	return client, nil
}
