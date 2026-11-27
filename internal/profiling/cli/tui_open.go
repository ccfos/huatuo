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

// Package cli defines command-line options and handlers shared by profiling commands.
package cli

import (
	"fmt"
	"os/signal"
	"strings"
	"syscall"

	ucli "github.com/urfave/cli/v2"

	tuioutput "github.com/ccfos/huatuo/internal/profiling/formatter/tui"
)

// TUIOpenFlagName is the canonical option for report replay.
const TUIOpenFlagName = "tui-open"

// TUIOpenFlag returns an independent --tui-open flag for each command.
func TUIOpenFlag() ucli.Flag {
	return &ucli.StringFlag{
		Name:  TUIOpenFlagName,
		Usage: "Open a saved profiling report from `FILE` in the terminal",
	}
}

// RunTUIOpen validates the replay arguments and runs the terminal viewer.
func RunTUIOpen(cliCtx *ucli.Context) error {
	path := cliCtx.String(TUIOpenFlagName)
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("--tui-open requires a report file path")
	}
	if cliCtx.Args().Present() {
		return fmt.Errorf("--tui-open does not accept positional arguments")
	}
	for _, flag := range cliCtx.Command.Flags {
		name := flag.Names()[0]
		if name != TUIOpenFlagName && cliCtx.IsSet(name) {
			return fmt.Errorf("--tui-open cannot be used with --%s", name)
		}
	}

	// Keep report preparation interruptible before the terminal viewer opens.
	ctx, stop := signal.NotifyContext(cliCtx.Context, syscall.SIGHUP, syscall.SIGQUIT)
	defer stop()
	return tuioutput.RunTerminalViewer(ctx, path)
}
