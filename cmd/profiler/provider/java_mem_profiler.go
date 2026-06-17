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

package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	executil "huatuo-bamai/internal/profiler/exec"
	"huatuo-bamai/internal/profiler/registry"
	javaruntime "huatuo-bamai/internal/profiler/runtime/java"
)

func init() {
	impl := &javaMemoryProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:        "mem",
		LangOrImpl:  "java",
		Description: "Java memory profiler using async-profiler",
		Impl:        impl,
		Aggregator:  impl.NewAggregator,
	})
}

var memProfileOutFile map[int]string

type javaMemoryProfiler struct{}

func (p *javaMemoryProfiler) NewAggregator(pctx *pcontext.ProfilerContext) *aggregator.Aggregator {
	return newJavaAggregator(pctx).Aggregator
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

	pids, err := javaruntime.ResolveJavaPids(pid, toolLimit, execPath, svrAddr, containerID)
	if err != nil {
		return err
	}

	if mode == "object_usage" {
		javaVersion, err := javaruntime.GetJavaVersion(pids[0])
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

	if err := javaruntime.PrepareJavaAgent(pids[0], toolPath); err != nil {
		return err
	}

	cmdResults := sampleJavaMemoryProcesses(pctx.Ctx, pids, toolPath, event, extraArgs, loopInterval)
	return javaruntime.CheckAsprofStarted(cmdResults)
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

		memProfileOutFile[pid] = javaruntime.HostViewPath(pid, outFile)

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

	pids, err := javaruntime.ResolveJavaPids(pid, 0, execPath, svrAddr, containerID)
	if err != nil {
		return err
	}

	stopRes := javaruntime.StopAsprofProcesses(pids, toolPath)
	return javaruntime.CheckCmdResultsAllSuccess(stopRes, "stop")
}

func (p *javaMemoryProfiler) ReadDataLoop(ctx context.Context, addRecord func(any)) {
	javaruntime.ReadCollapsedFilesLoop(ctx, memProfileOutFile, addRecord)
}
