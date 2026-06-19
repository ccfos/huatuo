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
	impl := &cpuJavaProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:          "cpu",
		LangOrImpl:    "java",
		Description:   "Java CPU profiler using async-profiler",
		Impl:          impl,
		NewAggregator: impl.NewAggregator,
	})
}

type cpuJavaProfiler struct {
	profileOutFile map[int]string
}

func (p *cpuJavaProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newJavaAggregator(pctx)
}

func (p *cpuJavaProfiler) Start(pctx *pcontext.ProfilerContext) error {
	pids, err := javaruntime.ResolveJavaPids(pctx.PID, pctx.ToolLimit, pctx.ExecPath, pctx.ServerAddress, pctx.ContainerID)
	if err != nil {
		return err
	}

	if err := javaruntime.PrepareJavaAgent(pids[0], pctx.ToolPath); err != nil {
		return err
	}

	baseArgs := []string{
		"--libpath", "/tmp/libasyncProfiler.so",
		"-i", fmt.Sprintf("%dms", 1000/pctx.Freq),
		"-j", "256",
		"--loop", "9",
		"-o", "collapsed",
	}

	p.profileOutFile, err = javaruntime.StartAsprofSampling(pctx.Ctx, &javaruntime.AsprofSamplingOption{
		Pids:          pids,
		ToolPath:      pctx.ToolPath,
		BaseArgs:      baseArgs,
		OutFilePrefix: "cpu",
	})
	return err
}

func (p *cpuJavaProfiler) Stop(pctx *pcontext.ProfilerContext) error {
	return javaruntime.StopJavaProfiler(pctx.Ctx, javaruntime.StopJavaProfilerOption{
		PID:         pctx.PID,
		ExecPath:    pctx.ExecPath,
		ServerAddr:  pctx.ServerAddress,
		ContainerID: pctx.ContainerID,
		ToolPath:    pctx.ToolPath,
	})
}

func (p *cpuJavaProfiler) ReadDataLoop(ctx context.Context, addRecord func(any)) error {
	javaruntime.ReadCollapsedFilesLoop(ctx, p.profileOutFile, addRecord)
	return nil
}
