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
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"

	"huatuo-bamai/internal/pod"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/forktrack"
	"huatuo-bamai/pkg/profiling"
)

func runBefore(ctx *cli.Context) error {
	if ctx.NumFlags() == 0 || (ctx.Args().Len() == 0 && ctx.NumFlags() == 1) {
		cli.ShowAppHelpAndExit(ctx, 0)
	}

	if ctx.Args().Len() > 0 {
		return fmt.Errorf("invalid config: cannot specify two or more values(e.g., --pid pid1 instead of: --pid pid1 pid2)")
	}

	loggingOpts := loggingOptions{
		verbose: ctx.Bool("verbose"),
		level:   ctx.String("log-level"),
		file:    ctx.String("log-file"),
		size:    ctx.Int("log-size"),
	}
	if err := validateLoggingOptions(loggingOpts, ctx.IsSet("log-size")); err != nil {
		return err
	}

	if ctx.String("type") == "" || ctx.String("language") == "" {
		return fmt.Errorf("missing required flags: --type and --language")
	}

	typ, err := profiling.ParseType(ctx.String("type"))
	if err != nil {
		return err
	}
	lang, err := profiling.ParseLanguage(ctx.String("language"))
	if err != nil {
		return err
	}
	if !profiling.IsSupported(lang, typ) {
		return fmt.Errorf("language %s does not support profiling type %s", lang, typ)
	}
	if err := validatePythonProfileOptions(lang, typ, ctx.Int("duration"), ctx.Int("aggr-interval")); err != nil {
		return err
	}
	if err := validateMemoryMode(lang, typ, ctx.String("memory-mode")); err != nil {
		return err
	}
	if err := validateProfilerFlagCompatibility(ctx, lang, typ); err != nil {
		return err
	}

	if err := validateLanguageOptions(ctx, lang, typ); err != nil {
		return err
	}

	if err := validateCommonOptions(ctx); err != nil {
		return err
	}

	closer, err := setupLogging(loggingOpts)
	if err != nil {
		return err
	}
	if closer != nil {
		if ctx.App.Metadata == nil {
			ctx.App.Metadata = make(map[string]any)
		}
		ctx.App.Metadata[loggingCloserKey] = closer
	}
	return nil
}

func validateLoggingOptions(opts loggingOptions, logSizeSet bool) error {
	if opts.verbose {
		opts.level = "debug"
		opts.file = "stdout"
	}

	switch opts.level {
	case "trace", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid --log-level %q; allowed: trace, debug, info, warn, error", opts.level)
	}
	if opts.file == "" {
		return fmt.Errorf("--log-file must be a file path or stdout")
	}
	if opts.size < 0 {
		return fmt.Errorf("--log-size must be at least 0 MB")
	}
	if logSizeSet && opts.file == "stdout" {
		return fmt.Errorf("--log-size applies only when --log-file is a file path")
	}
	return nil
}

func validateLanguageOptions(ctx *cli.Context, lang profiling.Language, typ profiling.Type) error {
	switch lang {
	case profiling.LanguageGo, profiling.LanguageC, profiling.LanguageCPP:
		if err := validateSinglePID(ctx, "native"); err != nil {
			return err
		}
		if typ == profiling.TypeMemory {
			return validateExactlyOneTarget(ctx)
		}

		return nil

	case profiling.LanguageJava:
		if ctx.String("tool-path") == "" {
			return fmt.Errorf("language=%s requires --tool-path", lang)
		}

		return validateExactlyOneTarget(ctx)

	case profiling.LanguagePython:
		if err := ensurePythonToolPath(ctx); err != nil {
			return err
		}
		return validateExactlyOneTarget(ctx)

	case profiling.LanguageUnknown:
		return fmt.Errorf("missing required flag: --language")

	default:
		return fmt.Errorf("unsupported language: %s", lang)
	}
}

func validatePythonProfileOptions(lang profiling.Language, typ profiling.Type, duration, interval int) error {
	if lang != profiling.LanguagePython {
		return nil
	}
	if typ != profiling.TypeCPU {
		return fmt.Errorf("Python profiler supports only --type=cpu")
	}
	if interval != duration {
		return fmt.Errorf(
			"Python CPU profiler does not support continuous profiling: --aggr-interval (%ds) must equal --duration (%ds)",
			interval,
			duration,
		)
	}
	return nil
}

func ensurePythonToolPath(ctx *cli.Context) error {
	if ctx.String("tool-path") != "" {
		return nil
	}
	return fmt.Errorf("language=python requires --tool-path")
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
	if err := validateNumericOptions(
		profiling.Type(ctx.String("type")),
		ctx.Int("freq"),
		ctx.Int("max-concurrent-procs"),
	); err != nil {
		return err
	}

	pids, err := pcontext.ParsePIDs(ctx.String("pid"))
	if err != nil {
		return err
	}
	for _, pid := range pids {
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

	if cpuidStr := ctx.String("cpuid"); cpuidStr != "" {
		if _, err := parseCPUIDs(cpuidStr); err != nil {
			return err
		}
	}

	if err := validateAggregationWindow(ctx.Int("duration"), ctx.Int("aggr-interval")); err != nil {
		return err
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
	if err := validateOutputFormat(ctx.String("output-format")); err != nil {
		return err
	}

	return nil
}

func validateNumericOptions(profileType profiling.Type, freq, maxProfilerProcesses int) error {
	if profileType == profiling.TypeCPU && freq < 1 {
		return fmt.Errorf("frequency must be at least 1 sample per second")
	}
	if maxProfilerProcesses < 0 {
		return fmt.Errorf("maximum profiler processes must not be negative")
	}
	return nil
}

func validateProfilerFlagCompatibility(ctx *cli.Context, lang profiling.Language, typ profiling.Type) error {
	implementation, _ := profiling.ImplementationFor(lang)
	native := implementation == profiling.ImplementationNative
	nativeCPU := native && typ == profiling.TypeCPU
	nativeMemory := native && typ == profiling.TypeMemory

	if lang == profiling.LanguageJava && typ == profiling.TypeCPU && ctx.Int("freq") > 1000 {
		return fmt.Errorf("Java profiler frequency must not exceed 1000 samples per second")
	}
	if ctx.String("cpuid") != "" && !nativeCPU {
		return fmt.Errorf("--cpuid is supported only by native CPU profiling")
	}
	if ctx.Bool("log-bpf-debug") && !native {
		return fmt.Errorf("--log-bpf-debug is supported only by native profilers")
	}
	if ctx.Bool("follow-forks") {
		if !native {
			return fmt.Errorf("--follow-forks is supported only by native profilers")
		}
		if ctx.String("pid") == "" {
			return fmt.Errorf("--follow-forks requires --pid")
		}
		maxTracked := uint64(ctx.Uint("fork-max-procs"))
		if maxTracked == 0 || maxTracked > uint64(forktrack.HardMaxTracked) {
			return fmt.Errorf("--fork-max-procs must be between 1 and %d", forktrack.HardMaxTracked)
		}
		if uint64(ctx.Uint("fork-rate")) > uint64(forktrack.MaxRate) {
			return fmt.Errorf("--fork-rate must not exceed %d", forktrack.MaxRate)
		}
		if uint64(ctx.Uint("fork-burst")) > uint64(forktrack.MaxBurst) {
			return fmt.Errorf("--fork-burst must not exceed %d", forktrack.MaxBurst)
		}
	} else if ctx.IsSet("fork-max-procs") || ctx.IsSet("fork-rate") || ctx.IsSet("fork-burst") {
		return fmt.Errorf("fork limit flags require --follow-forks")
	}
	if ctx.Bool("thread-group") && !native {
		return fmt.Errorf("--thread-group is supported only by native profiling")
	}
	if ctx.String("binary-match-path") != "" && native {
		return fmt.Errorf("--binary-match-path is not supported by native profilers")
	}
	if ctx.IsSet("physical-memory-probability") {
		physicalMemory := nativeMemory &&
			profiling.MemoryMode(ctx.String("memory-mode")) != profiling.MemoryModeVirtualAlloc
		if !physicalMemory {
			return fmt.Errorf("--physical-memory-probability is supported only by native physical memory profiling")
		}
		probability := ctx.Uint("physical-memory-probability")
		if probability < 1 || probability > 100 {
			return fmt.Errorf("physical memory probability must be between 1 and 100")
		}
	}
	return nil
}

func validateOutputFormat(format string) error {
	switch format {
	case "collapsed", "flamegraph", "svg", "remote":
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func validateMemoryMode(lang profiling.Language, typ profiling.Type, value string) error {
	if typ != profiling.TypeMemory {
		if value != "" {
			return fmt.Errorf("--memory-mode is only valid when --type=memory")
		}
		return nil
	}
	if value == "" {
		return fmt.Errorf("--memory-mode is required when --type=memory")
	}
	mode, err := profiling.ParseMemoryMode(value)
	if err != nil {
		return err
	}
	if profiling.SupportsMemoryMode(lang, mode) {
		return nil
	}
	supported := profiling.MemoryModesFor(lang)
	values := make([]string, 0, len(supported))
	for _, candidate := range supported {
		values = append(values, string(candidate))
	}
	return fmt.Errorf(
		"memory mode %q is not supported for %s; supported modes: %s",
		mode,
		lang,
		strings.Join(values, ", "),
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
