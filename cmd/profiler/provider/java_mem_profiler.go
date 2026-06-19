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

	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/registry"
	javaruntime "huatuo-bamai/internal/profiler/runtime/java"
)

func init() {
	impl := &javaMemoryProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:          "mem",
		LangOrImpl:    "java",
		Description:   "Java memory profiler using async-profiler",
		Impl:          impl,
		NewAggregator: impl.NewAggregator,
	})
}

type javaMemoryProfiler struct {
	profileOutFile map[int]string
}

func (p *javaMemoryProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newJavaAggregator(pctx)
}

func (p *javaMemoryProfiler) Start(pctx *pcontext.ProfilerContext) error {
	pids, err := javaruntime.ResolveJavaPids(pctx.PID, pctx.ToolLimit, pctx.ExecPath, pctx.ServerAddress, pctx.ContainerID)
	if err != nil {
		return err
	}

	mode := pctx.ExtraFlags["mode"]
	event := "alloc"

	var extraArgs []string

	if mode == "" {
		mode = "object_alloc"
	}

	if mode == "object_usage" {
		javaVersion, err := javaruntime.GetJavaVersion(pids[0])
		if err != nil {
			return fmt.Errorf("failed to get Java version for PID %d: %w", pids[0], err)
		}

		if javaVersion < 11 {
			return fmt.Errorf("object_usage mode only supports Java 11 or newer, current Java version is %d", javaVersion)
		}

		extraArgs = append(extraArgs, "--live")
	}

	if err := javaruntime.PrepareJavaAgent(pids[0], pctx.ToolPath); err != nil {
		return err
	}

	baseArgs := []string{
		"--libpath", "/tmp/libasyncProfiler.so",
		"-e", event,
		"--alloc", "512k",
		"-j", "256",
		"--loop", "9",
		"-o", "collapsed",
	}
	baseArgs = append(baseArgs, extraArgs...)

	p.profileOutFile, err = javaruntime.StartAsprofSampling(pctx.Ctx, &javaruntime.AsprofSamplingOption{
		Pids:          pids,
		ToolPath:      pctx.ToolPath,
		BaseArgs:      baseArgs,
		OutFilePrefix: "mem",
	})
	return err
}

func (p *javaMemoryProfiler) Stop(pctx *pcontext.ProfilerContext) error {
	return javaruntime.StopJavaProfiler(pctx.Ctx, javaruntime.StopJavaProfilerOption{
		PID:         pctx.PID,
		ExecPath:    pctx.ExecPath,
		ServerAddr:  pctx.ServerAddress,
		ContainerID: pctx.ContainerID,
		ToolPath:    pctx.ToolPath,
	})
}

func (p *javaMemoryProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	return javaruntime.ReadCollapsedFilesLoop(ctx, p.profileOutFile, enqueue)
}
