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

	_ "huatuo-bamai/cmd/profiler/cpu"
	_ "huatuo-bamai/cmd/profiler/mem"
	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/log"
	pcontext "huatuo-bamai/internal/profiler/context"
	pyhelper "huatuo-bamai/internal/profiler/helper/python"
	registry "huatuo-bamai/internal/profiler/registry/v2"
)

// initBpfManager prepares the shared BPF manager for native profilers. The
// returned cleanup must run on exit so map FDs and pinned objects are released.
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
				Usage: "Output format for profiling: raw|pprof|es|flamegraph|svg",
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

			if typ != "cpu" && typ != "mem" {
				return fmt.Errorf("unsupported profiling type: %q (expected: cpu or mem)", typ)
			}

			if err := validateLanguageOptions(ctx, lang, typ); err != nil {
				return err
			}

			return validateCommonOptions(ctx)
		},
		Action: func(cliCtx *cli.Context) error {
			typ := cliCtx.String("type")
			lang := cliCtx.String("language")

			if isNativeLang(lang) {
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

			return registry.Profile(pctx, meta)
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

func isNativeLang(lang string) bool {
	switch lang {
	case "go", "c", "c++":
		return true
	default:
		return false
	}
}

func validateLanguageOptions(ctx *cli.Context, lang, typ string) error {
	switch lang {
	case "go", "c", "c++":
		if typ == "mem" {
			return validateExactlyOneTarget(ctx)
		}

		return nil

	case "java":
		if ctx.String("tool-path") == "" {
			return fmt.Errorf("language=%s requires --tool-path", lang)
		}

		return validateExactlyOneTarget(ctx)

	case "python":
		if err := ensurePythonToolPath(ctx, typ); err != nil {
			return err
		}

		return validateExactlyOneTarget(ctx)

	case "":
		return fmt.Errorf("missing required flag: --language")

	default:
		return fmt.Errorf("unsupported language: %s", lang)
	}
}

// ensurePythonToolPath fills in a default --tool-path for python mem profiles
// (memray ships its own bundle dir) and otherwise enforces a user-supplied path.
func ensurePythonToolPath(ctx *cli.Context, typ string) error {
	if ctx.String("tool-path") != "" {
		return nil
	}

	if typ != "mem" {
		return fmt.Errorf("language=python requires --tool-path")
	}

	defaultToolPath, err := pyhelper.ResolveMemrayBundlePath("")
	if err != nil {
		return err
	}

	info, err := os.Stat(defaultToolPath)
	if err != nil {
		return fmt.Errorf("python mem profiler default tool-path invalid: %s: %w", defaultToolPath, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("python mem profiler default tool-path must be a directory: %s", defaultToolPath)
	}

	return nil
}

func validateExactlyOneTarget(ctx *cli.Context) error {
	hasContainer := ctx.String("container-id") != ""
	hasPID := ctx.Uint64("pid") != 0

	if hasContainer == hasPID {
		return fmt.Errorf("exactly one of --container-id or --pid must be provided")
	}

	return nil
}

func validateCommonOptions(ctx *cli.Context) error {
	if pid := ctx.Uint64("pid"); pid != 0 {
		procPath := fmt.Sprintf("/proc/%d", pid)
		if _, err := os.Stat(procPath); os.IsNotExist(err) {
			return fmt.Errorf("pid %d does not exist", pid)
		}
	}

	if cid := ctx.String("container-id"); cid != "" {
		if err := validateContainerID(cid); err != nil {
			return err
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
}

func validateContainerID(cid string) error {
	if len(cid) < 12 || len(cid) > 64 {
		return fmt.Errorf("invalid container-id length: %s (should be 12-64 characters)", cid)
	}

	for i, c := range cid {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return fmt.Errorf("container-id contains non-hex character at position %d: %c", i, c)
		}
	}

	return nil
}
