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
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/pcapfilter"
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
	//nolint:gosec // CLI flag names, not credentials.
	cliFlagDropwatchCorrelation = "dropwatch-correlation"
	//nolint:gosec // CLI flag names, not credentials.
	cliFlagDropwatchBPFPath = "dropwatch-bpf-path"
	//nolint:gosec // CLI flag names, not credentials.
	cliFlagDropwatchMaxEventsPerSecond = "dropwatch-max-events-per-second"
)

const (
	modeRetransmit = "retransmit"

	outputText = "text"
	outputJSON = "json"

	dropwatchCorrelationOff   = "off"
	dropwatchCorrelationLocal = "local"

	defaultDropwatchMaxEventsPerSecond = 100

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
		&cli.StringFlag{
			Name:  cliFlagDropwatchCorrelation,
			Value: dropwatchCorrelationOff,
			Usage: "correlate retransmissions with kernel drops: off or local",
		},
		&cli.StringFlag{
			Name:  cliFlagDropwatchBPFPath,
			Usage: "path to dropwatch.o; required for local correlation",
		},
		&cli.Uint64Flag{
			Name:  cliFlagDropwatchMaxEventsPerSecond,
			Value: defaultDropwatchMaxEventsPerSecond,
			Usage: "rate limit local drop events to N/sec (0 = unlimited)",
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
	switch correlation := c.String(cliFlagDropwatchCorrelation); correlation {
	case dropwatchCorrelationOff:
	case dropwatchCorrelationLocal:
		if c.String(cliFlagDropwatchBPFPath) == "" {
			return fmt.Errorf(
				"--dropwatch-bpf-path is required when --dropwatch-correlation=local",
			)
		}
		if err := pcapfilter.ValidateL3Compatible(effectiveFilter(c)); err != nil {
			return fmt.Errorf(
				"invalid --filter for --dropwatch-correlation=local: %w",
				err,
			)
		}
	default:
		return fmt.Errorf(
			"invalid --dropwatch-correlation %q; want off or local",
			correlation,
		)
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

// Local correlation uses one normalized expression so both probes observe the
// same traffic scope. An explicit TCP default avoids unrelated drop events.
func effectiveFilter(c *cli.Context) string {
	filter := strings.TrimSpace(c.String(cliFlagFilter))
	if filter == "" && c.String(cliFlagDropwatchCorrelation) == dropwatchCorrelationLocal {
		return "tcp"
	}
	return filter
}
