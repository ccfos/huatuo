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
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	executil "huatuo-bamai/internal/profiler/exec"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/pkg/profiling"
)

type pythonCPUProfiler struct {
	duration int
	freq     int
	toolPath string
	pids     []int
}

func init() {
	impl := &pythonCPUProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:           profiling.TypeCPU,
		Implementation: profiling.ImplementationPython,
		Description:    "Python CPU profiler using py-spy",
		Impl:           impl,
		NewAggregator:  impl.NewAggregator,
	})
}

// ManagesDuration marks py-spy as self-terminating after record --duration.
func (*pythonCPUProfiler) ManagesDuration() {}

func (p *pythonCPUProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newPythonCPUAggregator(pctx)
}

func (p *pythonCPUProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if err := validatePythonToolPath(pctx.ToolPath); err != nil {
		return err
	}
	if err := validatePythonAggregationWindow(pctx.Duration, pctx.AggrInterval); err != nil {
		return err
	}

	p.duration = pctx.Duration
	p.freq = pctx.Freq
	p.toolPath = pctx.ToolPath

	pids, err := resolvePythonPids(pctx)
	if err != nil {
		return err
	}
	if err := validateResolvedPIDs("Python", pids); err != nil {
		return err
	}
	if len(pctx.PIDs) > 0 {
		if err := validateProcessExecutables("Python", "python", pids); err != nil {
			return err
		}
		if err := validateExpectedExecPath(pids, pctx.ExecPath); err != nil {
			return err
		}
	}
	pids, err = pythonRootPids(pids, procutil.ParentPID)
	if err != nil {
		return err
	}
	if err := validateMaxProfilerProcesses("Python", pids, pctx.MaxProfilerProcesses); err != nil {
		return err
	}

	p.pids = pids

	return nil
}

func (p *pythonCPUProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	return runPySpyAndEmit(ctx, p.duration, p.freq, p.toolPath, p.pids, enqueue)
}

func (p *pythonCPUProfiler) Stop(_ *pcontext.ProfilerContext) error {
	return nil
}

func resolvePythonPids(pctx *pcontext.ProfilerContext) ([]int, error) {
	if len(pctx.PIDs) > 0 {
		return pctx.PIDs, nil
	}

	pids, err := procutil.GetPidsFromContainer(pctx.ExecPath, "python", pctx.ContainerID)
	if err != nil {
		return nil, err
	}

	if len(pids) == 0 {
		return nil, fmt.Errorf("sampling failed: no target Python processes found in container %q", pctx.ContainerID)
	}

	return pids, nil
}

func runPySpyAndEmit(ctx context.Context, dur, freq int, toolPath string, pids []int, enqueue func(any)) error {
	cmdResults := runPySpy(ctx, pids, dur, freq, toolPath)

	var errorMessages []string

	for i := range cmdResults {
		cmdRes := &cmdResults[i]
		targetPid := cmdRes.Pid

		if !cmdRes.Success {
			errorMessages = append(errorMessages,
				fmt.Sprintf("PID[%d] sampling failed: %v, stderr: %q", targetPid, cmdRes.CmdErr, string(cmdRes.Stderr)))

			continue
		}

		if len(cmdRes.Stdout) > 0 {
			enqueue(profiler.SampleOutput{
				PID:    targetPid,
				Output: string(cmdRes.Stdout),
			})
		}
	}

	if len(errorMessages) > 0 {
		return fmt.Errorf("sampling failed:\n%s", strings.Join(errorMessages, "\n"))
	}

	return nil
}

func runPySpy(ctx context.Context, pids []int, dur, freq int, pyspyPath string) []executil.CmdResult {
	pyspyBin := filepath.Join(pyspyPath, "py-spy")
	durStr := strconv.Itoa(dur)
	freqStr := strconv.Itoa(freq)

	return executil.ExecCmds(ctx, pids, pyspyBin, func(pid int) []string {
		return buildPySpyArgs(pid, durStr, freqStr)
	})
}

func buildPySpyArgs(pid int, duration, frequency string) []string {
	return []string{
		"record",
		"-d", duration,
		"-f", "raw",
		"-r", frequency,
		"--subprocesses",
		"-o", "/dev/stdout",
		"-p", strconv.Itoa(pid),
	}
}
