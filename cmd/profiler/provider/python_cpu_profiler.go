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
)

type pythonCPUProfiler struct{}

func init() {
	registry.RegisterProfilerMeta("python", "cpu", registry.ProfilerMeta{
		Type:        "cpu",
		LangOrImpl:  "python",
		Description: "Python CPU profiler using py-spy",
		Impl:        &pythonCPUProfiler{},
	})
}

// NewAggregator returns a no-op aggregator because py-spy is one-shot and the
// profiler creates and drives its own aggregator inside Start.
func (p *pythonCPUProfiler) NewAggregator(pctx *pcontext.ProfilerContext) *aggregator.Aggregator {
	return aggregator.NewAggregator(
		pctx,
		func(any) {},
		func(*pcontext.ProfilerContext) (any, error) { return nil, nil },
	)
}

// Start runs py-spy synchronously, owns its own aggregator end-to-end, then
// cancels pctx so registry.Profile's duration wait returns immediately.
func (p *pythonCPUProfiler) Start(pctx *pcontext.ProfilerContext) error {
	pctx.OneShotAgg = true

	aggr := newPythonCPUAggregator(pctx)
	aggr.Start()

	err := p.sample(pctx, func(so profiler.SampleOutput) {
		aggr.AddRecord(so)
	})

	aggr.Stop()
	pctx.Cancel()

	return err
}

func (p *pythonCPUProfiler) ReadDataLoop(_ context.Context, _ func(any)) {}

func (p *pythonCPUProfiler) Stop(_ *pcontext.ProfilerContext, _ *aggregator.Aggregator) error {
	return nil
}

func (p *pythonCPUProfiler) sample(pctx *pcontext.ProfilerContext, recordFn func(profiler.SampleOutput)) error {
	pid := pctx.PID
	freq := pctx.Freq
	dur := pctx.Duration
	toolPath := pctx.ToolPath
	toolLimit := pctx.ToolLimit
	execPath := pctx.ExecPath
	svrAddr := pctx.ServerAddress
	containerID := pctx.ContainerID

	var pids []int

	if pid != 0 {
		if execPath != "" {
			if err := procutil.CheckExecPath(pid, execPath); err != nil {
				return err
			}
		}
		pids = []int{pid}
	} else {
		var err error
		pids, err = procutil.GetPidsFromContainer(svrAddr, execPath, "python", containerID)
		if err != nil {
			return err
		}
		if toolLimit > 0 && len(pids) > toolLimit {
			return fmt.Errorf("sampling failed: too many target Python processes (limit: %d, found: %d)", toolLimit, len(pids))
		}
		if len(pids) == 0 {
			return fmt.Errorf("sampling failed: no target Python processes found in container: %s", containerID)
		}
	}

	cmdResults := runPySpy(pctx.Ctx, pids, dur, freq, toolPath)

	var errorMessages []string
	for i := range cmdResults {
		cmdRes := &cmdResults[i]
		targetPid := pids[i]
		if !cmdRes.Success {
			errorMessages = append(errorMessages,
				fmt.Sprintf("PID[%d] sampling failed: %v, stderr: %s", targetPid, cmdRes.CmdErr, string(cmdRes.Stderr)))
			continue
		}
		if len(cmdRes.Stdout) > 0 {
			recordFn(profiler.SampleOutput{
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

// runPySpy executes py-spy record in parallel for each PID, capturing raw folded-stack output.
func runPySpy(ctx context.Context, pids []int, dur, freq int, pyspyPath string) []executil.CmdResult {
	pyspyBin := filepath.Join(pyspyPath, "py-spy")
	durStr := strconv.Itoa(dur)
	freqStr := strconv.Itoa(freq)
	return executil.ExecCmds(ctx, pids, pyspyBin, func(pid int) []string {
		return []string{
			"record",
			"-d", durStr,
			"-f", "raw",
			"-r", freqStr,
			"--subprocesses",
			"-o", "/dev/stdout",
			"-p", strconv.Itoa(pid),
		}
	})
}
