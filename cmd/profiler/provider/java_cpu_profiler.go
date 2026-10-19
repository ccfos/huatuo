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

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/pkg/types"

	javaruntime "github.com/ccfos/huatuo/cmd/profiler/runtime/java"
	"github.com/ccfos/huatuo/internal/profiling/toolpath"
	"github.com/ccfos/huatuo/pkg/profiling"
)

type cpuJavaProfiler struct {
	config         Config
	profileOutFile map[int]string
	labels         map[int][]types.ProfileLabel
	samplingOpt    *javaruntime.AsprofSamplingOption
}

func (p *cpuJavaProfiler) Start(ctx context.Context) error {
	cfg := &p.config
	if err := validateJavaFrequency(cfg.Freq); err != nil {
		return err
	}

	if err := toolpath.Validate(profiling.LanguageJava, cfg.ToolDir); err != nil {
		return err
	}

	pids := cfg.PIDs
	if len(pids) == 0 {
		var err error
		pids, err = resolveJavaPIDs(
			cfg.ExecPath,
			cfg.ContainerID,
		)
		if err != nil {
			return err
		}
	}

	if err := validateResolvedPIDs("Java", pids); err != nil {
		return err
	}

	if len(cfg.PIDs) > 0 {
		if err := validateProcessExecutables("Java", "java", pids); err != nil {
			return err
		}

		if err := validateExpectedExecPath(pids, cfg.ExecPath); err != nil {
			return err
		}
	}

	if err := validateMaxProfilerProcesses("Java", pids, cfg.MaxProfilerProcesses); err != nil {
		return err
	}

	p.labels = make(map[int][]types.ProfileLabel, len(pids))
	for _, pid := range pids {
		labels, err := readProcessLabels(pid)
		if err != nil {
			return fmt.Errorf("read Java process %d metadata: %w", pid, err)
		}

		entry, err := javaProcessName(pid)
		if err != nil {
			return err
		}

		labels = append(labels, types.ProfileStringLabel("java_entry", entry))
		p.labels[pid] = labels
	}

	for _, pid := range pids {
		if err := javaruntime.PrepareJavaAgent(pid, cfg.ToolDir); err != nil {
			return fmt.Errorf("prepare Java agent for PID %d: %w", pid, err)
		}
	}

	baseArgs := []string{
		"--libpath", "/tmp/libasyncProfiler.so",
		"-i", fmt.Sprintf("%dms", 1000/cfg.Freq),
		"-j", "256",
	}

	opt := &javaruntime.AsprofSamplingOption{
		Pids:          pids,
		ToolPath:      cfg.ToolDir,
		BaseArgs:      baseArgs,
		OutFilePrefix: "cpu",
		AggrInterval:  javaAggregationInterval(cfg),
		Duration:      cfg.Duration,
	}
	profileOutFile, err := javaruntime.StartAsprofSampling(ctx, opt)
	if err != nil {
		return err
	}

	p.profileOutFile = profileOutFile
	p.samplingOpt = opt

	return nil
}

func (p *cpuJavaProfiler) Stop() error {
	return javaruntime.StopJavaProfiler(p.samplingOpt)
}

func javaAggregationInterval(cfg *Config) time.Duration {
	interval := cfg.AggregationInterval
	if interval <= 0 {
		return 10 * time.Second
	}

	return interval
}

func (p *cpuJavaProfiler) ReadDataLoop(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
	if p.samplingOpt == nil {
		return fmt.Errorf("read Java CPU profile: profiler is not started")
	}

	return javaruntime.ReadAsprofDataLoop(
		ctx,
		p.samplingOpt,
		p.profileOutFile,
		func(output profile.SampleOutput) error {
			return enqueueFolded(
				output,
				p.labels[output.PID],
				int64(1000/p.config.Freq)*int64(time.Millisecond),
				profiling.LanguageJava,
				enqueue,
			)
		},
	)
}
