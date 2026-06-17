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

import (
	"bytes"
	"fmt"
	"os"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/version"

	_ "huatuo-bamai/cmd/profiler/provider"
)

const profilerToolName = "profiler"

// Set by Makefile via -ldflags -X. Must live in package main; an empty
// value falls back to version.Devel via version.Resolve.
var (
	AppVersion   string
	AppGitCommit string
	AppBuildTime string
)

func main() {
	signalLog := &bytes.Buffer{}
	app := &cli.App{
		Name:          profilerToolName,
		Usage:         "Sample CPU and memory profiles for a process or container, with eBPF-based userland and Linux kernel stack collection",
		AllowExtFlags: true,
		Flags:         appFlags,
		Before:        runBefore,
		Action: func(cliCtx *cli.Context) error {
			return runAction(cliCtx, signalLog)
		},
	}

	version.Wire(app, version.Seed{
		Name:      profilerToolName,
		Version:   AppVersion,
		GitCommit: AppGitCommit,
		BuildTime: AppBuildTime,
	})

	if err := app.Run(os.Args); err != nil {
		if signalLog.Len() > 0 {
			fmt.Fprint(os.Stderr, signalLog.String())
		}

		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
