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

package provider

import (
	"context"
	"fmt"
	"time"

	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/registry"
	javaruntime "huatuo-bamai/internal/profiler/runtime/java"
	"huatuo-bamai/pkg/profiling"
)

func init() {
	impl := &cpuJavaProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:           profiling.TypeCPU,
		Implementation: profiling.ImplementationJava,
		Description:    "Java CPU profiler using async-profiler",
		Impl:           impl,
		NewAggregator:  impl.NewAggregator,
	})
}

type cpuJavaProfiler struct {
	profileOutFile map[int]string
	samplingOpt    *javaruntime.AsprofSamplingOption
}

func (p *cpuJavaProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newJavaAggregator(pctx)
}

func (p *cpuJavaProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if err := validateJavaFrequency(pctx.Freq); err != nil {
		return err
	}
	if err := validateJavaToolPath(pctx.ToolPath); err != nil {
		return err
	}

	pids := pctx.PIDs
	if len(pids) == 0 {
		var err error
		pids, err = javaruntime.ResolveJavaPids(
			pctx.ExecPath,
			pctx.ServerAddress,
			pctx.ContainerID,
		)
		if err != nil {
			return err
		}
	}
	if err := validateResolvedPIDs("Java", pids); err != nil {
		return err
	}
	if len(pctx.PIDs) > 0 {
		if err := validateProcessExecutables("Java", "java", pids); err != nil {
			return err
		}
		if err := validateExpectedExecPath(pids, pctx.ExecPath); err != nil {
			return err
		}
	}

	if err := validateMaxProfilerProcesses("Java", pids, pctx.MaxProfilerProcesses); err != nil {
		return err
	}

	for _, pid := range pids {
		if err := javaruntime.PrepareJavaAgent(pid, pctx.ToolPath); err != nil {
			return fmt.Errorf("prepare Java agent for PID %d: %w", pid, err)
		}
	}

	baseArgs := []string{
		"--libpath", "/tmp/libasyncProfiler.so",
		"-i", fmt.Sprintf("%dms", 1000/pctx.Freq),
		"-j", "256",
	}

	opt := &javaruntime.AsprofSamplingOption{
		Pids:          pids,
		ToolPath:      pctx.ToolPath,
		BaseArgs:      baseArgs,
		OutFilePrefix: "cpu",
		AggrInterval:  javaAggregationInterval(pctx),
		Duration:      time.Duration(pctx.Duration) * time.Second,
	}
	profileOutFile, err := javaruntime.StartAsprofSampling(pctx.Ctx, opt)
	if err != nil {
		return err
	}

	p.profileOutFile = profileOutFile
	p.samplingOpt = opt
	return nil
}

func (p *cpuJavaProfiler) Stop(pctx *pcontext.ProfilerContext) error {
	return javaruntime.StopJavaProfiler(pctx.Ctx, p.samplingOpt)
}

func javaAggregationInterval(pctx *pcontext.ProfilerContext) time.Duration {
	interval := time.Duration(pctx.AggrInterval) * time.Second
	if interval <= 0 {
		return 10 * time.Second
	}
	return interval
}

func (p *cpuJavaProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	if p.samplingOpt == nil {
		return fmt.Errorf("read Java CPU profile: profiler is not started")
	}
	return javaruntime.ReadAsprofDataLoop(ctx, p.samplingOpt, p.profileOutFile, enqueue)
}
