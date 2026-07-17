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

import "github.com/urfave/cli/v2"

var appFlags = []cli.Flag{
	&cli.StringFlag{
		Name:    "type",
		Aliases: []string{"t"},
		Usage:   "Profiling type: cpu|memory",
	},
	&cli.StringFlag{
		Name:    "language",
		Aliases: []string{"l"},
		Usage:   "Target language: java|go|python|c|c++",
	},
	&cli.StringFlag{
		Name:  "memory-mode",
		Usage: "Memory mode; Java: object_alloc|object_usage; native: virtual_alloc|physical_alloc|physical_usage",
	},
	&cli.StringFlag{
		Name:    "pid",
		Aliases: []string{"p"},
		Usage:   "Target PID(s), comma-separated for Java and Python; native supports one PID",
	},
	&cli.StringFlag{
		Name:  "cpuid",
		Usage: "CPU IDs to sample: comma-separated list and ranges (e.g., 1,3,5-10). Empty for all CPUs",
	},
	&cli.StringFlag{
		Name:  "container-id",
		Usage: "Target container ID",
	},
	&cli.StringFlag{
		Name:  "scope",
		Value: "thread",
		Usage: "Sampling dimension: thread|thread-group|process-group etc.",
	},
	&cli.IntFlag{
		Name:    "freq",
		Aliases: []string{"F"},
		Usage:   "The number of samples to collect per second",
		Value:   99,
	},
	&cli.UintFlag{
		Name:  "physical-memory-probability",
		Usage: "Native physical-memory sampling probability, from 1 to 100 percent",
		Value: 100,
	},
	&cli.IntFlag{
		Name:  "max-concurrent-procs",
		Usage: "Maximum concurrent profiler subprocesses; 0 means unlimited",
	},
	&cli.IntFlag{
		Name:  "aggr-interval",
		Usage: "interval for profiling of aggregate process",
		Value: 10,
	},
	&cli.IntFlag{
		Name:    "duration",
		Aliases: []string{"d"},
		Usage:   "Profiling duration in seconds",
		Value:   10,
	},
	&cli.StringFlag{
		Name:  "output-path",
		Usage: "Output path for profiling",
		Value: ".",
	},
	&cli.StringFlag{
		Name:  "output-format",
		Usage: "Output format for profiling: collapsed|flamegraph|svg|remote",
		Value: "collapsed",
	},
	&cli.StringFlag{
		Name:  "output-storage",
		Usage: "Unix socket path for remote upload (used with --output-format=remote)",
		Value: "/var/run/huatuo-toolstream.sock",
	},
	&cli.StringFlag{
		Name:  "log-level",
		Usage: "Log level: trace|debug|info|warn|error",
		Value: "error",
	},
	&cli.StringFlag{
		Name:  "log-file",
		Usage: "Log output destination: file path for rotating logs, or \"stdout\" for standard output",
		Value: "stdout",
	},
	&cli.IntFlag{
		Name:  "log-size",
		Usage: "Log rotation size in MB; 0 disables rotation",
		Value: 100,
	},
	&cli.BoolFlag{
		Name:  "log-bpf-debug",
		Usage: "Log bpf_dbg events (native profiler only)",
	},
	&cli.BoolFlag{
		Name:  "verbose",
		Usage: "Shorthand for --log-level debug --log-file stdout; overrides explicit values of both flags",
	},
	&cli.BoolFlag{
		Name:  "enable-pprof",
		Usage: "Serve Go runtime profiles on port 6000",
	},
	&cli.StringFlag{
		Name:  "tool-path",
		Usage: "Profiling tool root; Java expects bin/asprof and lib/libasyncProfiler.so",
	},
	&cli.StringFlag{
		Name:  "exec-path",
		Usage: "Executable path of target process",
	},
	&cli.StringFlag{
		Name:  "server-address",
		Usage: "Huatuo profiling server address",
		Value: "127.0.0.1:19704",
	},
	&cli.StringFlag{
		Name:  "tracer-id",
		Usage: "Tracing task ID; generated automatically when empty",
	},
}
