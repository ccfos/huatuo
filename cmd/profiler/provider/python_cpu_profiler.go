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

package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/process"
	"github.com/ccfos/huatuo/internal/profiler"
	profilerexec "github.com/ccfos/huatuo/internal/profiler/exec"
	profilerprocess "github.com/ccfos/huatuo/internal/profiler/process"
	"github.com/ccfos/huatuo/internal/profiler/toolpath"
	"github.com/ccfos/huatuo/pkg/profiling"
)

type pythonCPUProfiler struct {
	config Config
	pids   []int
}

// ManagesDuration marks py-spy as self-terminating after record --duration.
func (*pythonCPUProfiler) ManagesDuration() {}

func (p *pythonCPUProfiler) Start(_ context.Context) error {
	cfg := &p.config
	if err := toolpath.Validate(profiling.LanguagePython, cfg.ToolDir); err != nil {
		return err
	}
	if err := validatePythonAggregationWindow(cfg.Duration, cfg.AggregationInterval); err != nil {
		return err
	}

	if cfg.Duration <= 0 || cfg.Duration%time.Second != 0 {
		return fmt.Errorf("Python profiling duration must be a positive whole number of seconds: %s", cfg.Duration)
	}

	pids, err := resolvePythonPids(cfg)
	if err != nil {
		return err
	}
	if err := validateResolvedPIDs("Python", pids); err != nil {
		return err
	}
	if len(cfg.PIDs) > 0 {
		if err := validateProcessExecutables("Python", "python", pids); err != nil {
			return err
		}
		if err := validateExpectedExecPath(pids, cfg.ExecPath); err != nil {
			return err
		}
	}
	pids, err = pythonRootPids(pids, process.PPID)
	if err != nil {
		return err
	}
	if err := validateMaxProfilerProcesses("Python", pids, cfg.MaxProfilerProcesses); err != nil {
		return err
	}

	p.pids = pids

	return nil
}

func (p *pythonCPUProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	cmdResults := runPySpy(ctx, p.pids, p.config.Duration, p.config.Freq, p.config.ToolDir)

	var errorMessages []string

	for i := range cmdResults {
		cmdRes := cmdResults[i]
		targetPid := cmdRes.PID

		if !cmdRes.Succeeded() {
			errorMessages = append(errorMessages,
				fmt.Sprintf(
					"PID[%d] sampling failed: %v, diagnostics: %q",
					targetPid,
					cmdRes.Err,
					string(cmdRes.Diagnostics),
				))

			continue
		}

		if len(bytes.TrimSpace(cmdRes.Output)) == 0 {
			errorMessages = append(errorMessages,
				fmt.Sprintf("PID[%d] sampling failed: no samples collected", targetPid))
			continue
		}
		enqueue(profiler.SampleOutput{
			PID:    targetPid,
			Output: string(cmdRes.Output),
		})
	}

	if len(errorMessages) > 0 {
		return fmt.Errorf("sampling failed:\n%s", strings.Join(errorMessages, "\n"))
	}

	return nil
}

func (p *pythonCPUProfiler) Stop(_ context.Context) error {
	return nil
}

func resolvePythonPids(cfg *Config) ([]int, error) {
	if len(cfg.PIDs) > 0 {
		return cfg.PIDs, nil
	}

	pids, err := profilerprocess.ContainerRootPIDs(cfg.ContainerID, profilerprocess.ExecutableFilter{
		ExecutablePrefix: "python",
		ExecutablePath:   cfg.ExecPath,
	})
	if err != nil {
		return nil, err
	}

	if len(pids) == 0 {
		return nil, fmt.Errorf("sampling failed: no target Python processes found in container %q", cfg.ContainerID)
	}

	return pids, nil
}

func runPySpy(
	ctx context.Context,
	pids []int,
	duration time.Duration,
	freq int,
	pyspyPath string,
) []*profilerexec.Result {
	pyspyBin := filepath.Join(pyspyPath, "py-spy")
	durStr := strconv.FormatInt(int64(duration/time.Second), 10)
	freqStr := strconv.Itoa(freq)

	return profilerexec.RunWithMemfd(ctx, pids, pyspyBin, func(pid int, outputPath string) []string {
		return buildPySpyArgs(pid, durStr, freqStr, outputPath)
	}, executil.WithStdout(io.Discard))
}

func buildPySpyArgs(pid int, duration, frequency, outputPath string) []string {
	return []string{
		"record",
		"-d", duration,
		"-f", "raw",
		"-r", frequency,
		"--subprocesses",
		"-o", outputPath,
		"-p", strconv.Itoa(pid),
	}
}
