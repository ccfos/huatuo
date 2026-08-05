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
	"time"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/toolstream"
)

const (
	cliFlagBpfPath            = "bpf-path"
	cliFlagFilter             = "filter"
	cliFlagDevice             = "device"
	cliFlagDeviceExcluded     = "device-excluded"
	cliFlagDuration           = "duration"
	cliFlagOutput             = "output"
	cliFlagOutputStorage      = "output-storage"
	cliFlagTaskID             = "task-id"
	cliFlagMaxEventsPerSecond = "max-events-per-second"
	cliFlagSourceTypes        = "source-types"
)

const (
	outputText = "text"
	outputJSON = "json"

	maxDurationSeconds = int64(1<<63-1) / int64(time.Second)
)

func appFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     cliFlagBpfPath,
			Usage:    "path to compiled BPF object",
			Required: true,
		},
		&cli.StringFlag{
			Name:  cliFlagFilter,
			Usage: `pcap filter expression (e.g. "tcp and port 80")`,
		},
		&cli.StringFlag{
			Name:  cliFlagDevice,
			Usage: "whitelist interfaces, comma-separated; SKBs without a net_device are dropped",
		},
		&cli.StringFlag{
			Name:  cliFlagDeviceExcluded,
			Usage: "blacklist interfaces, comma-separated; SKBs without a net_device pass",
		},
		&cli.IntFlag{
			Name:  cliFlagDuration,
			Value: 0,
			Usage: "stop after N seconds (0 = run forever)",
		},
		&cli.StringFlag{
			Name:  cliFlagOutput,
			Value: outputText,
			Usage: "output format: text|json",
		},
		&cli.StringFlag{
			Name:  cliFlagOutputStorage,
			Usage: "unix socket path for event sink; overrides --output",
		},
		&cli.StringFlag{
			Name:  cliFlagTaskID,
			Usage: "task ID associated with this session (requires --output-storage)",
		},
		&cli.Uint64Flag{
			Name:  cliFlagMaxEventsPerSecond,
			Usage: "rate limit to N events/sec (0 = unlimited)",
			Value: 0,
		},
		&cli.StringFlag{
			Name:   cliFlagSourceTypes,
			Value:  toolstream.SourceTypeTool,
			Hidden: true,
		},
	}
}

func validateFlags(c *cli.Context) error {
	if c.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %q", c.Args().Slice())
	}
	if v := c.String(cliFlagOutput); v != outputJSON && v != outputText {
		return fmt.Errorf("invalid --output %q; want json or text", v)
	}
	if c.String(cliFlagDevice) != "" && c.String(cliFlagDeviceExcluded) != "" {
		return errors.New("--device and --device-excluded are mutually exclusive")
	}
	if duration := c.Int(cliFlagDuration); duration < 0 || int64(duration) > maxDurationSeconds {
		return fmt.Errorf("invalid --duration %d; want 0..%d seconds", duration, maxDurationSeconds)
	}
	switch sourceType := c.String(cliFlagSourceTypes); sourceType {
	case toolstream.SourceTypeEvent, toolstream.SourceTypeTool:
	default:
		return fmt.Errorf(
			"invalid --source-types %q; want %q or %q",
			sourceType,
			toolstream.SourceTypeTool,
			toolstream.SourceTypeEvent,
		)
	}
	if c.String(cliFlagTaskID) != "" && c.String(cliFlagOutputStorage) == "" {
		return errors.New("--task-id requires --output-storage")
	}
	if c.IsSet(cliFlagOutput) && c.String(cliFlagOutputStorage) != "" {
		if _, err := fmt.Fprintln(c.App.ErrWriter, "warning: --output is ignored because --output-storage is set"); err != nil {
			return fmt.Errorf("write warning: %w", err)
		}
	}
	return nil
}
