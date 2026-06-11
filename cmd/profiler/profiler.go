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
	"bytes"
	"fmt"
	"os"

	_ "huatuo-bamai/cmd/profiler/cpu"
	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	pcontext "huatuo-bamai/internal/profiler/context"
	registry "huatuo-bamai/internal/profiler/registry"

	"github.com/urfave/cli/v2"
)

func initBpfManager(duration int) (func(), error) {
	if err := bpf.NewManager(&bpf.Option{
		KeepaliveTimeout: duration,
	}); err != nil {
		return nil, fmt.Errorf("init bpf manager err: %w", err)
	}

	return func() {
		bpf.Close()
	}, nil
}

func main() {
	signalLog := &bytes.Buffer{}
	app := &cli.App{
		Name:          "profiler",
		Usage:         "Cross-language profiling CLI",
		AllowExtFlags: true,
		Flags: []cli.Flag{
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
				Usage:   "Profiling type: cpu|mem|lock",
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
				Usage: "Enable verbose logging",
			},
			&cli.StringFlag{
				Name:  "log-path",
				Usage: "Default log path of profiling",
			},
			&cli.IntFlag{
				Name:  "log-size",
				Usage: "Default log size of profiling",
				Value: 100,
			},
			&cli.StringFlag{
				Name:  "tool-path",
				Usage: "Path to the profiling tool (e.g. async-profiler, py-spy)",
			},
			&cli.IntFlag{
				Name:  "tool-limit",
				Usage: "Limit how many third-party tools can run in parallel",
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
				Usage:   "Extra cpu/memory/lock profiler flags",
			},
			&cli.StringFlag{
				Name:  "output-path",
				Usage: "Output path for profiling",
				Value: ".",
			},
			&cli.StringFlag{
				Name:  "output-format",
				Usage: "Output format for profiling: raw|pprof|es|flamegraph|svg|flamedata",
				Value: "raw",
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
				Usage: "Meta data for document data",
			},
			&cli.StringFlag{
				Name:  "mock-container",
				Usage: "Mock container metadata JSON for uploads (testing only), or 'random' to auto-generate",
			},
			&cli.StringSliceFlag{
				Name:  "cpuidle-metadata",
				Usage: "Meta data for cpuidle tracerData",
			},
			&cli.StringSliceFlag{
				Name:  "cpusys-metadata",
				Usage: "Meta data for cpusys tracerData",
			},
		},
		Before: func(ctx *cli.Context) error {
			if ctx.NumFlags() == 0 || (ctx.Args().Len() == 0 && ctx.NumFlags() == 1) {
				cli.ShowAppHelpAndExit(ctx, 0)
			}
			if ctx.Args().Len() > 0 {
				return fmt.Errorf("invalid config: cannot specify two or more values(e.g., --pid pid1 instead of: --pid pid1 pid2)")
			}

			log.SetupProfilerLogger(ctx.Bool("verbose"), ctx.String("log-path"), ctx.Int("log-size"))

			typ := ctx.String("type")
			lang := ctx.String("language")

			if typ == "" || lang == "" {
				return fmt.Errorf("missing required flags: --type and --language")
			}

			if typ != "cpu" && typ != "mem" && typ != "lock" {
				return fmt.Errorf("unsupported profiling type: %q (expected: cpu or mem or lock)", typ)
			}

			switch lang {
			case "go", "c", "c++":
				if typ == "mem" {
					if (ctx.String("container-id") == "" && ctx.Uint64("pid") == 0) ||
						(ctx.String("container-id") != "" && ctx.Uint64("pid") != 0) {
						return fmt.Errorf("exactly one of --container-id or --pid must be provided")
					}
				}
			case "java":
				if ctx.String("tool-path") == "" {
					return fmt.Errorf("language=%s requires --tool-path", lang)
				}
				fallthrough
			case "python":
				if lang == "python" && ctx.String("tool-path") == "" {
					return fmt.Errorf("language=%s requires --tool-path", lang)
				}
				if (ctx.String("container-id") == "" && ctx.Uint64("pid") == 0) ||
					(ctx.String("container-id") != "" && ctx.Uint64("pid") != 0) {
					return fmt.Errorf("exactly one of --container-id or --pid must be provided")
				}
			case "":
				return fmt.Errorf("missing required flag: --language")
			default:
				return fmt.Errorf("unsupported language: %s", lang)
			}

			if pid := ctx.Uint64("pid"); pid != 0 {
				procPath := fmt.Sprintf("/proc/%d", pid)
				if _, err := os.Stat(procPath); os.IsNotExist(err) {
					return fmt.Errorf("pid %d does not exist", pid)
				}
			}

			if cid := ctx.String("container-id"); cid != "" {
				if len(cid) < 12 || len(cid) > 64 {
					return fmt.Errorf("invalid container-id length: %s (should be 12-64 characters)", cid)
				}
				for i, c := range cid {
					if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
						return fmt.Errorf("container-id contains non-hex character at position %d: %c", i, c)
					}
				}
			}

			if d := ctx.Int("duration"); d < 1 {
				return fmt.Errorf("duration must be at least 1 second")
			}

			scope := ctx.String("scope")
			validScopes := map[string]bool{"thread": true, "thread-group": true, "process-group": true}
			if !validScopes[scope] {
				return fmt.Errorf("unsupported scope: %s (allowed: thread, thread-group, process-group)", scope)
			}

			if toolPath := ctx.String("tool-path"); toolPath != "" {
				info, err := os.Stat(toolPath)
				if err != nil {
					return fmt.Errorf("tool-path does not exist: %s", toolPath)
				}
				if !info.IsDir() {
					return fmt.Errorf("tool-path must be a directory: %s", toolPath)
				}
			}

			return nil
		},
		Action: func(cliCtx *cli.Context) error {
			typ := cliCtx.String("type")
			lang := cliCtx.String("language")

			switch lang {
			case "go", "c", "c++":
				cleanup, err := initBpfManager(cliCtx.Int("duration"))
				if err != nil {
					return err
				}
				defer cleanup()
			}

			pctx, err := pcontext.NewProfilerContext(cliCtx, signalLog)
			if err != nil {
				return err
			}
			defer pctx.Cancel()

			meta, err := registry.GetProfiler(lang, typ)
			if err != nil {
				return err
			}
			log.P().Infof("using profiler: %s-%s (%s)", meta.LangOrImpl, meta.Type, meta.Description)

			err = registry.Profile(pctx, meta)
			if err != nil {
				return err
			}

			return nil
		},
	}

	if err := app.Run(os.Args); err != nil {
		if signalLog.Len() > 0 {
			fmt.Fprint(os.Stderr, signalLog.String())
		}

		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}