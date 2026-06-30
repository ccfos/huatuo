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

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/log"
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
)

const (
	outputText = "text"
	outputJSON = "json"
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
	}
}

func validateFlags(c *cli.Context) error {
	if v := c.String(cliFlagOutput); v != outputJSON && v != outputText {
		return fmt.Errorf("--output: invalid value %q, want json or text", v)
	}
	if c.IsSet(cliFlagOutput) && c.String(cliFlagOutputStorage) != "" {
		log.Warnf("--output is ignored because --output-storage is set")
	}
	if c.String(cliFlagDevice) != "" && c.String(cliFlagDeviceExcluded) != "" {
		return errors.New("--device and --device-excluded are mutually exclusive")
	}
	return nil
}
