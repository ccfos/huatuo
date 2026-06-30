// Copyright 2025 The HuaTuo Authors
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
		Name:  "server-address",
		Usage: "Huatuo profiling server address",
		Value: "127.0.0.1:19704",
	},
	&cli.IntFlag{
		Name:    "duration",
		Aliases: []string{"d"},
		Usage:   "Profiling duration in seconds",
		Value:   10,
	},
	&cli.StringFlag{
		Name:    "language",
		Aliases: []string{"l"},
		Usage:   "Target language: java|go|python|c|c++",
	},
	&cli.StringFlag{
		Name:    "type",
		Aliases: []string{"t"},
		Usage:   "Profiling type: cpu|mem",
	},
	&cli.Uint64Flag{
		Name:    "pid",
		Aliases: []string{"p"},
		Usage:   "Target PID",
	},
	&cli.StringFlag{
		Name:  "container-id",
		Usage: "Target container ID",
	},
	&cli.StringFlag{
		Name:  "exec-path",
		Usage: "Executable path of target process",
	},
	&cli.StringFlag{
		Name:  "scope",
		Value: "thread",
		Usage: "Sampling dimension: thread|thread-group|process-group etc.",
	},
	&cli.BoolFlag{
		Name:  "verbose",
		Usage: "Shorthand for --log-level debug --log-file stdout; overrides explicit values of both flags",
	},
	&cli.StringFlag{
		Name:  "log-level",
		Usage: "Log level: trace|debug|info|warn|error",
		Value: "info",
	},
	&cli.StringFlag{
		Name:  "log-file",
		Usage: "Log output destination: file path for rotating logs, or \"stdout\" for standard output",
		Value: "stdout",
	},
	&cli.IntFlag{
		Name:  "log-size",
		Usage: "Default log size of profiling",
		Value: 100,
	},
	&cli.BoolFlag{
		Name:  "log-bpf-debug",
		Usage: "Log bpf_dbg events (native profiler only)",
	},
	&cli.StringFlag{
		Name:  "tool-path",
		Usage: "Path to the profiling tool (e.g. async-profiler, py-spy)",
	},
	&cli.IntFlag{
		Name:  "tool-limit",
		Usage: "Limit how many third-party tools can run in parallel (e.g. async-profiler, py-spy)",
	},
	&cli.IntFlag{
		Name:    "freq",
		Aliases: []string{"F"},
		Usage:   "The number of samples to collect per second",
		Value:   99,
	},
	&cli.StringSliceFlag{
		Name:    "flags",
		Aliases: []string{"f"},
		Usage:   "Extra cpu/memory profiler flags, e.g. -f '--core-id=10' -f '--title=AppName'",
	},
	&cli.StringFlag{
		Name:  "output-path",
		Usage: "Output path for profiling",
		Value: ".",
	},
	&cli.StringFlag{
		Name:  "output-format",
		Usage: "Output format for profiling: collapsed|pprof|es|flamegraph|svg",
		Value: "collapsed",
	},
	&cli.IntFlag{
		Name:  "aggr-interval",
		Usage: "interval for profiling of aggregate process",
		Value: 10,
	},
	&cli.StringFlag{
		Name:  "es-address",
		Usage: "address for ES client",
	},
	&cli.StringFlag{
		Name:  "es-username",
		Usage: "username for ES client",
	},
	&cli.StringFlag{
		Name:  "es-password",
		Usage: "password for ES client",
	},
	&cli.StringFlag{
		Name:  "es-index",
		Usage: "index for ES client",
	},
	&cli.StringSliceFlag{
		Name:  "metadata",
		Usage: "Meta data for document data, e.g. --metadata '--tracer_id HHKKJGKIUOLNK' --metadata '--tracer_data=AppName'",
	},
	&cli.StringFlag{
		Name:  "mock-container",
		Usage: "Mock container metadata JSON for uploads (testing only), or 'random' to auto-generate",
	},
	&cli.StringSliceFlag{
		Name:  "cpuidle-metadata",
		Usage: "Meta data for cpuidle tracerData, e.g. --cpuidle-metadata '--user_threshold 54' --cpuidle-metadata '--user=AppName'",
	},
	&cli.StringSliceFlag{
		Name:  "cpusys-metadata",
		Usage: "Meta data for cpusys tracerData, e.g. --cpusys-metadata '--usage_threshold 33' --cpusys-metadata '--title=AppName'",
	},
}
