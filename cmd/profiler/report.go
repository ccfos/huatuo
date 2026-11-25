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
	"os/signal"
	"strings"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/ccfos/huatuo/internal/profiling/formatter/flamegraph"
)

func runTUIReport(cliCtx *cli.Context) error {
	path := cliCtx.String(tuiReportFlag)
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("--tui-report requires a report file path")
	}
	if cliCtx.Args().Present() {
		return fmt.Errorf("--tui-report does not accept positional arguments")
	}
	for _, flag := range appFlags {
		name := flag.Names()[0]
		if name != tuiReportFlag && cliCtx.IsSet(name) {
			return fmt.Errorf("--tui-report cannot be used with --%s", name)
		}
	}

	// Let the viewer restore the terminal on signals Bubble Tea does not handle.
	ctx, stop := signal.NotifyContext(cliCtx.Context, syscall.SIGHUP, syscall.SIGQUIT)
	defer stop()
	return flamegraph.Report(ctx, path)
}
