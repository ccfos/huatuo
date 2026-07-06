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
	"fmt"
	"os"
	"runtime"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/pod"
	pyruntime "huatuo-bamai/internal/profiler/runtime/python"
)

func runBefore(ctx *cli.Context) error {
	if ctx.NumFlags() == 0 || (ctx.Args().Len() == 0 && ctx.NumFlags() == 1) {
		cli.ShowAppHelpAndExit(ctx, 0)
	}

	if ctx.Args().Len() > 0 {
		return fmt.Errorf("invalid config: cannot specify two or more values(e.g., --pid pid1 instead of: --pid pid1 pid2)")
	}

	setupLogging(loggingOptions{
		verbose: ctx.Bool("verbose"),
		level:   ctx.String("log-level"),
		file:    ctx.String("log-file"),
		size:    ctx.Int("log-size"),
	})

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

	defaultToolPath, err := pyruntime.ResolveMemrayBundlePath("")
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
		if err := pod.ValidateContainerID(cid); err != nil {
			return err
		}
	}

	if cpuid := ctx.Int("cpuid"); cpuid >= 0 {
		if cpuid >= runtime.NumCPU() {
			return fmt.Errorf("cpuid %d is out of range (available: 0-%d)", cpuid, runtime.NumCPU()-1)
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

	if ctx.String("output-format") == "remote" && ctx.String("output-storage") == "" {
		return fmt.Errorf("--output-storage must not be empty when --output-format=remote")
	}

	return nil
}
