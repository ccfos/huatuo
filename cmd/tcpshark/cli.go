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
	"time"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/toolstream"
)

const (
	cliFlagMode               = "mode"
	cliFlagEnableTLP          = "enable-tlp"
	cliFlagBpfPath            = "bpf-path"
	cliFlagFilter             = "filter"
	cliFlagDuration           = "duration"
	cliFlagOutput             = "output"
	cliFlagOutputStorage      = "output-storage"
	cliFlagTaskID             = "task-id"
	cliFlagMaxEventsPerSecond = "max-events-per-second"
	cliFlagSourceTypes        = "source-types"
)

const (
	modeRetransmit = "retransmit"

	outputText = "text"
	outputJSON = "json"

	maxDurationSeconds = int64(1<<63-1) / int64(time.Second)
)

func appFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     cliFlagMode,
			Usage:    "capture mode: retransmit",
			Required: true,
		},
		&cli.BoolFlag{
			Name:    cliFlagEnableTLP,
			Aliases: []string{"tlp"},
			Usage:   "include Tail Loss Probe events in retransmit mode",
		},
		&cli.StringFlag{
			Name:     cliFlagBpfPath,
			Usage:    "path to the BPF object file for the selected mode",
			Required: true,
		},
		&cli.StringFlag{
			Name:  cliFlagFilter,
			Usage: "pcap filter expression (tcpdump syntax); empty = all TCP retransmissions",
		},
		&cli.IntFlag{
			Name:  cliFlagDuration,
			Usage: "run for N seconds then exit (0=forever)",
		},
		&cli.Uint64Flag{
			Name:  cliFlagMaxEventsPerSecond,
			Usage: "rate limit to N events/sec (0 = unlimited)",
		},
		&cli.StringFlag{
			Name:  cliFlagOutput,
			Value: outputText,
			Usage: "output format: json or text; ignored when --output-storage is set",
		},
		&cli.StringFlag{
			Name:  cliFlagOutputStorage,
			Usage: "unix socket path to send events to; when set, --output is ignored",
		},
		&cli.StringFlag{
			Name:  cliFlagTaskID,
			Usage: "task ID to associate with this session (requires --output-storage)",
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
	if mode := c.String(cliFlagMode); mode != modeRetransmit {
		return fmt.Errorf("invalid --mode %q; want %q", mode, modeRetransmit)
	}
	if duration := c.Int(cliFlagDuration); duration < 0 || int64(duration) > maxDurationSeconds {
		return fmt.Errorf("invalid --duration %d; want 0..%d seconds", duration, maxDurationSeconds)
	}
	if outputFormat := c.String(cliFlagOutput); outputFormat != outputJSON && outputFormat != outputText {
		return fmt.Errorf("invalid --output %q; want json or text", outputFormat)
	}
	if taskID := c.String(cliFlagTaskID); taskID != "" && c.String(cliFlagOutputStorage) == "" {
		return fmt.Errorf("--task-id requires --output-storage")
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
	if c.IsSet(cliFlagOutput) && c.String(cliFlagOutputStorage) != "" {
		if _, err := fmt.Fprintln(c.App.ErrWriter, "warning: --output is ignored because --output-storage is set"); err != nil {
			return fmt.Errorf("write warning: %w", err)
		}
	}
	return nil
}
