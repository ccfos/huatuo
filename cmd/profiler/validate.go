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
	"fmt"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/pod"
	pcontext "huatuo-bamai/internal/profiler/context"
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

	if typ != "cpu" && typ != "mem" && typ != "lock" {
		return fmt.Errorf("unsupported profiling type: %q (expected: cpu, mem, or lock)", typ)
	}
	if err := validateMemoryMode(lang, typ, ctx.String("memory-mode")); err != nil {
		return err
	}

	if err := validateLanguageOptions(ctx, lang, typ); err != nil {
		return err
	}

	return validateCommonOptions(ctx)
}

func validateLanguageOptions(ctx *cli.Context, lang, typ string) error {
	switch lang {
	case "go", "c", "c++":
		if err := validateSinglePID(ctx, "native"); err != nil {
			return err
		}
		if typ == "mem" {
			return validateNativeMemoryTarget(ctx)
		}

		return nil

	case "java":
		if typ == "lock" {
			return fmt.Errorf("kernel lock profiling requires language go, c, or c++")
		}
		if ctx.String("tool-path") == "" {
			return fmt.Errorf("language=%s requires --tool-path", lang)
		}

		return validateExactlyOneTarget(ctx)

	case "python":
		if typ == "lock" {
			return fmt.Errorf("kernel lock profiling requires language go, c, or c++")
		}
		if err := validateSinglePID(ctx, "Python"); err != nil {
			return err
		}
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

func validateNativeMemoryTarget(ctx *cli.Context) error {
	hasContainer := ctx.String("container-id") != ""
	hasPID := ctx.String("pid") != ""
	hasCgroup := ctx.Uint64("cgroup-id") != 0 || ctx.String("cgroup-path") != ""
	hasProcessGroup := ctx.Int("process-group-id") != 0

	targets := 0
	for _, present := range []bool{hasContainer, hasPID, hasCgroup, hasProcessGroup} {
		if present {
			targets++
		}
	}
	if targets != 1 {
		return fmt.Errorf("exactly one PID/TGID, container/cgroup, or process group target must be provided")
	}

	return nil
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
	hasPID := ctx.String("pid") != ""

	if hasContainer == hasPID {
		return fmt.Errorf("exactly one of --container-id or --pid must be provided")
	}

	return nil
}

func validateSinglePID(ctx *cli.Context, profilerName string) error {
	pids, err := pcontext.ParsePIDs(ctx.String("pid"))
	if err != nil {
		return err
	}
	if len(pids) > 1 {
		return fmt.Errorf("%s profiler does not support multiple PIDs", profilerName)
	}
	return nil
}

func validateCommonOptions(ctx *cli.Context) error {
	pids, err := pcontext.ParsePIDs(ctx.String("pid"))
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if uint64(pid) > math.MaxInt32 {
			return fmt.Errorf("pid %d exceeds Linux PID range", pid)
		}
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

	if cgroupPath := strings.TrimSpace(ctx.String("cgroup-path")); cgroupPath != "" {
		info, err := os.Stat(cgroupPath)
		if err != nil {
			return fmt.Errorf("cgroup-path does not exist: %s: %w", cgroupPath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("cgroup-path must be a directory: %s", cgroupPath)
		}
	}
	if ctx.Uint64("cgroup-id") != 0 && ctx.String("cgroup-path") != "" {
		return fmt.Errorf("only one of --cgroup-id or --cgroup-path may be provided")
	}
	if pgid := ctx.Int("process-group-id"); pgid < 0 || uint64(pgid) > math.MaxInt32 {
		return fmt.Errorf("process-group-id must be between 0 and %d", math.MaxInt32)
	}

	if cpuidStr := ctx.String("cpuid"); cpuidStr != "" {
		if _, err := parseCPUIDs(cpuidStr); err != nil {
			return err
		}
	}

	if err := validateAggregationWindow(ctx.Int("duration"), ctx.Int("aggr-interval")); err != nil {
		return err
	}

	scope, err := pcontext.NormalizeScope(ctx.String("scope"))
	if err != nil {
		return err
	}
	switch scope {
	case pcontext.ScopePID, pcontext.ScopeTGID:
		if ctx.IsSet("scope") && len(pids) == 0 {
			return fmt.Errorf("scope %s requires --pid", scope)
		}
		if ctx.IsSet("scope") && len(pids) > 1 {
			return fmt.Errorf("scope %s requires exactly one --pid", scope)
		}
	case pcontext.ScopeCgroup:
		if ctx.String("container-id") == "" && ctx.Uint64("cgroup-id") == 0 && ctx.String("cgroup-path") == "" {
			return fmt.Errorf("scope cgroup requires --container-id, --cgroup-id, or --cgroup-path")
		}
	case pcontext.ScopeProcessGroup:
		if ctx.Int("process-group-id") == 0 && len(pids) == 0 {
			return fmt.Errorf("scope process-group requires --process-group-id or --pid")
		}
		if ctx.Int("process-group-id") == 0 && len(pids) > 1 {
			return fmt.Errorf("scope process-group cannot derive one group from multiple PIDs")
		}
	}

	if _, err := pcontext.ParseLockTypes(ctx.String("lock-types")); err != nil {
		return err
	}
	if mode := ctx.String("lock-mode"); mode != "time" && mode != "count" {
		return fmt.Errorf("invalid lock-mode %q (allowed: time, count)", mode)
	}
	if ctx.Duration("lock-min-wait") < 0 || ctx.Duration("lock-min-wait") > time.Hour {
		return fmt.Errorf("lock-min-wait must be between 0 and 1h")
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

func validateMemoryMode(lang, typ, mode string) error {
	if typ != "mem" {
		if mode != "" {
			return fmt.Errorf("--memory-mode is only valid when --type=mem")
		}
		return nil
	}
	if mode == "" {
		return fmt.Errorf("--memory-mode is required when --type=mem")
	}

	var supported []string
	switch lang {
	case "java":
		supported = []string{"object_alloc", "object_usage"}
	case "go", "c", "c++":
		supported = []string{"virtual_alloc", "physical_alloc", "physical_usage"}
	case "python":
		return fmt.Errorf("Python memory profiler does not support --memory-mode yet")
	default:
		return nil
	}

	for _, candidate := range supported {
		if mode == candidate {
			return nil
		}
	}
	return fmt.Errorf(
		"memory mode %q is not supported for %s; supported modes: %s",
		mode,
		lang,
		strings.Join(supported, ", "),
	)
}

func validateAggregationWindow(duration, interval int) error {
	if duration < 1 {
		return fmt.Errorf("duration must be at least 1 second")
	}
	if interval < 1 {
		return fmt.Errorf("aggregation interval must be at least 1 second")
	}
	if interval > duration {
		return fmt.Errorf(
			"aggregation interval (%ds) exceeds duration (%ds)",
			interval,
			duration,
		)
	}
	return nil
}

func parseCPUIDs(s string) ([]int, error) {
	return parseCPUIDsWithLimit(s, runtime.NumCPU())
}

func parseCPUIDsWithLimit(s string, numCPU int) ([]int, error) {
	if numCPU <= 0 {
		return nil, fmt.Errorf("cpu count must be positive")
	}

	var cpuIDs []int
	seen := make(map[int]bool)

	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid cpuid range: %q", part)
			}

			start, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid cpuid range start: %q", rangeParts[0])
			}

			end, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid cpuid range end: %q", rangeParts[1])
			}

			if start > end {
				return nil, fmt.Errorf("invalid cpuid range: start %d > end %d", start, end)
			}

			for i := start; i <= end; i++ {
				if i < 0 || i >= numCPU {
					return nil, fmt.Errorf("cpuid %d is out of range (available: 0-%d)", i, numCPU-1)
				}
				if !seen[i] {
					seen[i] = true
					cpuIDs = append(cpuIDs, i)
				}
			}
		} else {
			id, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid cpuid: %q", part)
			}

			if id < 0 || id >= numCPU {
				return nil, fmt.Errorf("cpuid %d is out of range (available: 0-%d)", id, numCPU-1)
			}

			if !seen[id] {
				seen[id] = true
				cpuIDs = append(cpuIDs, id)
			}
		}
	}

	if len(cpuIDs) == 0 {
		return nil, fmt.Errorf("cpuid list is empty")
	}

	return cpuIDs, nil
}
