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

package mem

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
		Type:        "mem",
		LangOrImpl:  "java",
		Description: "Java memory profiler using async-profiler",
		Impl:        newJavaMemoryProfiler(),
	}

	registry.RegisterProfilerMeta(meta.LangOrImpl, meta.Type, meta)
}

var memProfileOutFile map[int]string

type javaMemoryProfiler struct{}

func newJavaMemoryProfiler() registry.Profiler {
	return &javaMemoryProfiler{}
}

func (p *javaMemoryProfiler) NewAggregator(pctx *pcontext.ProfilerContext) *aggregator.Aggregator {
	return agghr.NewJavaAggregator(pctx).Aggregator
}

func (p *javaMemoryProfiler) Start(pctx *pcontext.ProfilerContext) error {
	pid := pctx.PID
	toolPath := pctx.ToolPath
	toolLimit := pctx.ToolLimit
	execPath := pctx.ExecPath
	svrAddr := pctx.ServerAddress
	containerID := pctx.ContainerID
	mode := pctx.ExtraFlags["mode"]
	event := "alloc"

	var extraArgs []string

	if mode == "" {
		mode = "object_alloc"
	}

	pids, err := helper.ResolveJavaPids(pid, toolLimit, execPath, svrAddr, containerID)
	if err != nil {
		return err
	}

	if mode == "object_usage" {
		javaVersion, err := helper.GetJavaVersion(pids[0])
		if err != nil {
			return fmt.Errorf("failed to get Java version for PID %d: %w", pids[0], err)
		}

		// --live requires Java 11+: keeps only objects still referenced.
		if javaVersion < 11 {
			return fmt.Errorf("object_usage mode only supports Java 11 or newer, current Java version is %d", javaVersion)
		}

		extraArgs = append(extraArgs, "--live")
	}

	loopInterval := 9

	if err := helper.PrepareJavaAgent(pids[0], toolPath); err != nil {
		return err
	}

	cmdResults := sampleJavaMemoryProcesses(pctx.Ctx, pids, toolPath, event, extraArgs, loopInterval)
	return helper.CheckAsprofStarted(cmdResults)
}

func sampleJavaMemoryProcesses(ctx context.Context, pids []int, asprofPath, event string, extraArgs []string, loopInterval int) []executil.CmdResult {
	asprofBin := filepath.Join(asprofPath, "asprof")

	baseArgs := []string{
		"--libpath", "/tmp/libasyncProfiler.so",
		"-e", event,
		"--alloc", "512k",
		// Set the maximum Java stack depth to minimize stack storage
		"-j", "256",
		"--loop", strconv.Itoa(loopInterval),
		"-o", "collapsed",
	}

	if len(extraArgs) > 0 {
		baseArgs = append(baseArgs, extraArgs...)
	}

	return executil.ExecCmds(ctx, pids, asprofBin, func(pid int) []string {
		args := append([]string{}, baseArgs...)

		outFile := fmt.Sprintf("/tmp/asprof-mem-%d.collapsed", pid)
		args = append(args, "-f", outFile, strconv.Itoa(pid))

		if memProfileOutFile == nil {
			memProfileOutFile = make(map[int]string)
		}

		memProfileOutFile[pid] = helper.HostViewPath(pid, outFile)

		return args
	})
}

// Stop profiling, abnormal Stop also goes through here
func (p *javaMemoryProfiler) Stop(pctx *pcontext.ProfilerContext, aggregator *aggregator.Aggregator) error {
	pid := pctx.PID
	toolPath := pctx.ToolPath
	execPath := pctx.ExecPath
	svrAddr := pctx.ServerAddress
	containerID := pctx.ContainerID

	aggregator.Stop()

	pids, err := helper.ResolveJavaPids(pid, 0, execPath, svrAddr, containerID)
	if err != nil {
		return err
	}

	stopRes := stopSampleProcesses(pids, toolPath)
	return helper.CheckCmdResultsAllSuccess(stopRes, "stop")
}

func (p *javaMemoryProfiler) ReadDataLoop(ctx context.Context, addRecord func(any)) {
	helper.ReadCollapsedFilesLoop(ctx, memProfileOutFile, addRecord)
}

func stopSampleProcesses(pids []int, toolPath string) []executil.CmdResult {
	return helper.StopAsprofProcesses(pids, toolPath)
}
