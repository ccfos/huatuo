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
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/cmd/tcpshark/retransmit"
	"github.com/ccfos/huatuo/internal/dropwatch"
	"github.com/ccfos/huatuo/internal/pcapfilter"
	"github.com/ccfos/huatuo/internal/toolstream"
)

const (
	cliFlagMode               = "mode"
	cliFlagEnableTLP          = "enable-tlp"
	cliFlagBPFPath            = "bpf-path"
	cliFlagBPFPathDir         = "bpf-path-dir"
	cliFlagWithDropwatch      = "with-dropwatch"
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
	modeRetransmit = "retransmit"

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
			Name:  cliFlagBPFPath,
			Usage: "path to tcp_retransmit.o; required without --with-dropwatch",
		},
		&cli.StringFlag{
			Name:  cliFlagBPFPathDir,
			Usage: "directory containing tcp_retransmit.o and net_dropwatch.o",
		},
		&cli.BoolFlag{
			Name:  cliFlagWithDropwatch,
			Usage: "correlate retransmissions with embedded dropwatch",
		},
		&cli.StringFlag{
			Name: cliFlagFilter,
			Usage: "pcap filter expression; empty = all retransmissions, " +
				"or tcp with --with-dropwatch",
		},
		&cli.IntFlag{
			Name:  cliFlagDuration,
			Usage: "run for N seconds then exit (0=forever)",
		},
		&cli.StringFlag{
			Name: cliFlagDevice,
			Usage: "whitelist dropwatch interfaces, comma-separated; SKBs without a net_device are dropped " +
				"(requires --with-dropwatch)",
		},
		&cli.StringFlag{
			Name: cliFlagDeviceExcluded,
			Usage: "blacklist dropwatch interfaces, comma-separated; SKBs without a net_device pass " +
				"(requires --with-dropwatch)",
		},
		&cli.Uint64Flag{
			Name: cliFlagMaxEventsPerSecond,
			Usage: "rate limit each enabled BPF input to N events/sec " +
				"(0 = unlimited)",
		},
		&cli.StringFlag{
			Name:  cliFlagOutput,
			Value: retransmit.OutputText,
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
	outputStorage := c.String(cliFlagOutputStorage)
	if outputStorage == "" {
		switch output := c.String(cliFlagOutput); output {
		case retransmit.OutputJSON, retransmit.OutputText:
		default:
			return fmt.Errorf("invalid --output %q; want json or text", output)
		}
	}
	if taskID := c.String(cliFlagTaskID); taskID != "" && outputStorage == "" {
		return errors.New("--task-id requires --output-storage")
	}
	device := c.String(cliFlagDevice)
	deviceExcluded := c.String(cliFlagDeviceExcluded)
	if device != "" && deviceExcluded != "" {
		return errors.New("--device and --device-excluded are mutually exclusive")
	}
	if (device != "" || deviceExcluded != "") && !c.Bool(cliFlagWithDropwatch) {
		return errors.New("--device and --device-excluded require --with-dropwatch")
	}
	bpfPath := strings.TrimSpace(c.String(cliFlagBPFPath))
	bpfPathDir := strings.TrimSpace(c.String(cliFlagBPFPathDir))
	if c.Bool(cliFlagWithDropwatch) {
		if bpfPath != "" {
			return errors.New("--bpf-path cannot be used with --with-dropwatch; use --bpf-path-dir")
		}
		if bpfPathDir == "" {
			return errors.New("--bpf-path-dir is required with --with-dropwatch")
		}
	} else {
		if bpfPathDir != "" {
			return errors.New("--bpf-path-dir requires --with-dropwatch")
		}
		if bpfPath == "" {
			return errors.New("--bpf-path is required without --with-dropwatch")
		}
	}
	if filter := resolveFilterExpression(c); filter != "" {
		if err := pcapfilter.ValidateL3Compatible(filter); err != nil {
			if errors.Is(err, pcapfilter.ErrL3IncompatibleFilter) {
				return errors.New(
					"invalid --filter for synthetic retransmit packet: ethernet header fields are unavailable",
				)
			}
			return fmt.Errorf("invalid --filter for synthetic retransmit packet: %w", err)
		}
	}
	if c.IsSet(cliFlagOutput) && outputStorage != "" {
		if _, err := fmt.Fprintln(c.App.ErrWriter, "warning: --output is ignored because --output-storage is set"); err != nil {
			return fmt.Errorf("write warning: %w", err)
		}
	}
	return nil
}

// Embedded correlation uses one normalized expression so both probes observe the
// same traffic scope. An explicit TCP default avoids unrelated drop events.
func resolveFilterExpression(c *cli.Context) string {
	filter := strings.TrimSpace(c.String(cliFlagFilter))
	if filter == "" && c.Bool(cliFlagWithDropwatch) {
		return "tcp"
	}
	return filter
}

// resolveRunOptions consumes validated flags so feature code does not need
// to interpret CLI path combinations or filter defaults.
func resolveRunOptions(c *cli.Context, version string) runOptions {
	cfg := retransmit.RunConfig{
		Tracing: retransmit.Config{
			BPFPath:            strings.TrimSpace(c.String(cliFlagBPFPath)),
			FilterExpression:   resolveFilterExpression(c),
			MaxEventsPerSecond: c.Uint64(cliFlagMaxEventsPerSecond),
			TLPEnabled:         c.Bool(cliFlagEnableTLP),
		},
		SourceType:    c.String(cliFlagSourceTypes),
		Output:        c.App.Writer,
		OutputFormat:  c.String(cliFlagOutput),
		OutputStorage: c.String(cliFlagOutputStorage),
		ToolName:      tcpSharkToolName,
		Version:       version,
		TaskID:        c.String(cliFlagTaskID),
	}
	if c.Bool(cliFlagWithDropwatch) {
		directory := strings.TrimSpace(c.String(cliFlagBPFPathDir))
		cfg.Tracing.BPFPath = filepath.Join(directory, "tcp_retransmit.o")
		cfg.Dropwatch = &dropwatch.Config{
			BPFPath:            filepath.Join(directory, "net_dropwatch.o"),
			FilterExpression:   cfg.Tracing.FilterExpression,
			MaxEventsPerSecond: cfg.Tracing.MaxEventsPerSecond,
			HardwareMode:       dropwatch.HardwareAuto,
		}
		if device := c.String(cliFlagDevice); device != "" {
			cfg.Dropwatch.IncludeDevices = strings.Split(device, ",")
		}
		if deviceExcluded := c.String(cliFlagDeviceExcluded); deviceExcluded != "" {
			cfg.Dropwatch.ExcludeDevices = strings.Split(deviceExcluded, ",")
		}
	}
	return runOptions{
		mode:            c.String(cliFlagMode),
		durationSeconds: c.Int(cliFlagDuration),
		retransmit:      cfg,
	}
}
