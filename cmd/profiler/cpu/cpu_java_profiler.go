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

package cpu

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"huatuo-bamai/internal/profiler/aggregator"
	agghr "huatuo-bamai/internal/profiler/aggregator/handler"
	pcontext "huatuo-bamai/internal/profiler/context"
	executil "huatuo-bamai/internal/profiler/exec"
	helper "huatuo-bamai/internal/profiler/helper/java"
	registry "huatuo-bamai/internal/profiler/registry/v2"
)

func init() {
	meta := registry.ProfilerMeta{
		Type:        "cpu",
		LangOrImpl:  "java",
		Description: "Java CPU profiler using async-profiler",
		Impl:        newCPUJavaProfiler(),
	}

	registry.RegisterProfilerMeta(meta.LangOrImpl, meta.Type, meta)
}

var profileOutFile map[int]string

type cpuJavaProfiler struct{}

func newCPUJavaProfiler() registry.Profiler {
	return &cpuJavaProfiler{}
}

func (p *cpuJavaProfiler) NewAggregator(pctx *pcontext.ProfilerContext) *aggregator.Aggregator {
	return agghr.NewJavaAggregator(pctx).Aggregator
}

// Start async-profiler cmd
func (p *cpuJavaProfiler) Start(pctx *pcontext.ProfilerContext) error {
	pid := pctx.PID
	freq := pctx.Freq
	toolPath := pctx.ToolPath
	toolLimit := pctx.ToolLimit
	execPath := pctx.ExecPath
	svrAddr := pctx.ServerAddress
	containerID := pctx.ContainerID

	pids, err := helper.ResolveJavaPids(pid, toolLimit, execPath, svrAddr, containerID)
	if err != nil {
		return err
	}

	targetPid := pids[0]

	if err := helper.PrepareJavaAgent(targetPid, toolPath); err != nil {
		return err
	}

	// Sample and get results for all PIDs
	cmdResults := sampleJavaProcesses(pctx.Ctx, pids, freq, toolPath)
	return helper.CheckAsprofStarted(cmdResults)
}

// Executes multiple asprof instances for profiling
func sampleJavaProcesses(ctx context.Context, pids []int, freq int, asprofPath string) []executil.CmdResult {
	asprofBin := filepath.Join(asprofPath, "asprof")

	// interval = integer(1000ms/freq)
	intervalMs := 1000 / freq

	baseArgs := []string{
		"--libpath", "/tmp/libasyncProfiler.so",
		"-i", fmt.Sprintf("%dms", intervalMs),
		// Set the maximum Java stack depth to minimize stack storage
		"-j", "256",
		"--loop", "9",
		"-o", "collapsed",
	}

	return executil.ExecCmds(ctx, pids, asprofBin, func(pid int) []string {
		args := append([]string{}, baseArgs...)

		// append -f parameter (file for each pid)
		outFile := fmt.Sprintf("/tmp/asprof-cpu-%d.collapsed", pid)
		args = append(
			args,
			"-f", outFile,
			strconv.Itoa(pid),
		)
		if profileOutFile == nil {
			profileOutFile = make(map[int]string)
		}

		profileOutFile[pid] = helper.HostViewPath(pid, outFile)

		return args
	})
}

// Stop profiling, abnormal Stop also goes through here
func (p *cpuJavaProfiler) Stop(pctx *pcontext.ProfilerContext, aggregator *aggregator.Aggregator) error {
	pid := pctx.PID
	toolPath := pctx.ToolPath
	execPath := pctx.ExecPath
	svrAddr := pctx.ServerAddress
	containerID := pctx.ContainerID

	var pids []int

	// stop the aggregator
	aggregator.Stop()

	// stop async-profiler cmd
	pids, err := helper.ResolveJavaPids(pid, 0, execPath, svrAddr, containerID)
	if err != nil {
		return err
	}

	stopRes := stopSampleProcesses(pids, toolPath)
	return helper.CheckCmdResultsAllSuccess(stopRes, "stop")
}

// read data loop, pass data to aggregator
func (p *cpuJavaProfiler) ReadDataLoop(ctx context.Context, addRecord func(any)) {
	helper.ReadCollapsedFilesLoop(ctx, profileOutFile, addRecord)
}

func stopSampleProcesses(pids []int, toolPath string) []executil.CmdResult {
	return helper.StopAsprofProcesses(pids, toolPath)
}
