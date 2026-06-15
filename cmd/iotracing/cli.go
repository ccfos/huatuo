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
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"
)

// Flag names. Centralized so that registration and lookup cannot drift.
const (
	cliFlagBpfPath        = "bpf-path"
	cliFlagDevice         = "device"
	cliFlagOutput         = "output"
	cliFlagOutputStorage  = "output-storage"
	cliFlagTaskID         = "task-id"
	cliFlagMaxStack       = "max-stack"
	cliFlagMaxProcess     = "max-process"
	cliFlagMaxFilesPerPid = "max-files-per-process"
	cliFlagSchedThreshold = "schedule-threshold"
	cliFlagDuration       = "duration"
)

const (
	outputText = "text"
	outputJSON = "json"
)

func appFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:     cliFlagBpfPath,
			Required: true,
			Usage:    "path to the iotracing BPF object file",
		},
		&cli.StringFlag{
			Name:  cliFlagDevice,
			Usage: "filter by device(s) (format: major:minor, multiple devices separated by comma, e.g., 8:0 or 8:0,253:0)",
		},
		&cli.StringFlag{
			Name:  cliFlagOutput,
			Value: outputText,
			Usage: "output format: json or text; mutually exclusive with --output-storage",
		},
		&cli.IntFlag{
			Name:  cliFlagMaxStack,
			Value: 10,
			Usage: "keep at most N most-recent IO stack traces (older samples are evicted when the window overflows)",
		},
		&cli.IntFlag{
			Name:  cliFlagMaxProcess,
			Value: 10,
			Usage: "maximum number of top processes to display",
		},
		&cli.IntFlag{
			Name:  cliFlagMaxFilesPerPid,
			Value: 5,
			Usage: "maximum number of top files per process to display",
		},
		&cli.Uint64Flag{
			Name:  cliFlagSchedThreshold,
			Value: 100,
			Usage: "IO schedule threshold in milliseconds",
		},
		&cli.Uint64Flag{
			Name:  cliFlagDuration,
			Value: 8,
			Usage: "tool duration in seconds",
		},
		&cli.StringFlag{
			Name:  cliFlagOutputStorage,
			Usage: "unix socket path to send events to; mutually exclusive with --output",
		},
		&cli.StringFlag{
			Name:  cliFlagTaskID,
			Usage: "task ID to associate with this session (requires --output-storage)",
		},
	}
}

// validateFlags rejects invalid --output values and the --output / --output-storage
// conflict before the BPF stack is touched.
func validateFlags(c *cli.Context) error {
	if v := c.String(cliFlagOutput); v != outputJSON && v != outputText {
		return fmt.Errorf("--output: invalid value %q, want json or text", v)
	}

	if c.IsSet(cliFlagOutput) && c.String(cliFlagOutputStorage) != "" {
		return errors.New("--output and --output-storage are mutually exclusive")
	}

	return nil
}

// loadConfig reads CLI flags into ioConfig and the BPF filter map. The
// filter map is a separate return because it crosses the BPF ABI; keeping
// it out of ioConfig avoids accidental mutation by the data layer.
func loadConfig(c *cli.Context) (ioConfig, map[string]any, error) {
	cfg := ioConfig{
		maxStack:           c.Uint64(cliFlagMaxStack),
		maxProcess:         c.Uint64(cliFlagMaxProcess),
		maxFilesPerProcess: c.Uint64(cliFlagMaxFilesPerPid),
		scheduleThreshold:  c.Uint64(cliFlagSchedThreshold),
		durationSecond:     c.Uint64(cliFlagDuration),
	}

	if cfg.durationSecond == 0 {
		return ioConfig{}, nil, errors.New("--duration must be greater than zero")
	}

	if cfg.scheduleThreshold == 0 {
		return ioConfig{}, nil, errors.New("--schedule-threshold must be greater than zero")
	}

	filters := map[string]any{
		bpfFilterEventTimeout: cfg.scheduleThreshold * 1000 * 1000,
	}

	if deviceStr := c.String(cliFlagDevice); deviceStr != "" {
		deviceNums, err := parseDeviceNumbers(deviceStr)
		if err != nil {
			return ioConfig{}, nil, fmt.Errorf("parse device numbers: %w", err)
		}

		var deviceArray [bpfFilterDevMaxNums]uint32
		copy(deviceArray[:], deviceNums)

		filters[bpfFilterDevIDs] = deviceArray
		filters[bpfFilterDevCount] = uint32(len(deviceNums))
	}

	return cfg, filters, nil
}

// parseDeviceNumbers turns a "major:minor[,major:minor]" string into the
// kernel device-number encoding (major & 0xfff) << 20 | minor used by the
// BPF program's FILTER_DEVS array.
func parseDeviceNumbers(deviceStr string) ([]uint32, error) {
	var deviceNums []uint32

	for _, spec := range strings.Split(deviceStr, ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}

		parts := strings.Split(spec, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid device format: %s", spec)
		}

		major, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid major number %q: %w", parts[0], err)
		}

		minor, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid minor number %q: %w", parts[1], err)
		}

		deviceNums = append(deviceNums, (uint32(major)&0xfff)<<20|uint32(minor))
	}

	if len(deviceNums) > bpfFilterDevMaxNums {
		return nil, fmt.Errorf("too many devices specified (max %d), got %d", bpfFilterDevMaxNums, len(deviceNums))
	}

	return deviceNums, nil
}
