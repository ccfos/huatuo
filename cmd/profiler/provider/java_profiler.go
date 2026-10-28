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

const (
	javaAllocInterval    = "512k"
	javaMemoryStackDepth = "256"
)

type javaProfiler struct {
	config         Config
	profileOutFile map[int]string
	labels         map[int][]types.ProfileLabel
	samplingOpt    *javaruntime.AsprofSamplingOption
	collector      *javaruntime.CollapsedFileCollector
}

func (p *javaProfiler) Start(ctx context.Context) error {
	cfg := &p.config
	baseArgs := []string{"--libpath", "/tmp/libasyncProfiler.so"}
	var outFilePrefix string
	switch cfg.Type {
	case profiling.TypeCPU:
		if err := validateJavaFrequency(cfg.Freq); err != nil {
			return err
		}
		baseArgs = append(baseArgs, "-i", fmt.Sprintf("%dms", 1000/cfg.Freq), "-j", "256")
		outFilePrefix = "cpu"
	case profiling.TypeMemory:
		extraArgs, err := validateJavaMemoryMode(cfg.Mode)
		if err != nil {
			return err
		}
		baseArgs = append(baseArgs, "-e", "alloc", "--alloc", javaAllocInterval, "-j", javaMemoryStackDepth)
		baseArgs = append(baseArgs, extraArgs...)
		outFilePrefix = "mem"
	default:
		return fmt.Errorf("start Java profiler: unsupported profile type %q", cfg.Type)
	}

	pids, labels, err := prepareJavaSampling(cfg)
	if err != nil {
		return err
	}

	opt := &javaruntime.AsprofSamplingOption{
		Pids:          pids,
		ToolPath:      cfg.ToolDir,
		BaseArgs:      baseArgs,
		OutFilePrefix: outFilePrefix,
		AggrInterval:  javaAggregationInterval(cfg),
		Duration:      cfg.Duration,
	}
	profileOutFile, err := javaruntime.StartAsprofSampling(ctx, opt)
	if err != nil {
		return err
	}

	p.labels = labels
	p.profileOutFile = profileOutFile
	p.samplingOpt = opt

	return nil
}

func (p *javaProfiler) Stop() error {
	return javaruntime.StopJavaProfiler(p.samplingOpt)
}

func (p *javaProfiler) ReadDataLoop(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
	if p.samplingOpt == nil {
		return fmt.Errorf("read Java profile: profiler is not started")
	}

	p.collector = javaruntime.NewCollapsedFileCollector(p.samplingOpt.Pids, p.profileOutFile)

	return p.collector.ReadDataLoop(ctx, p.enqueueOutput(enqueue))
}

func (p *javaProfiler) Finalize(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
	return p.collector.Finalize(ctx, p.samplingOpt, p.enqueueOutput(enqueue))
}

func (p *javaProfiler) enqueueOutput(enqueue func(*pipeline.SampleBatch) error) func(profile.SampleOutput) error {
	multiplier := int64(1)
	if p.config.Type == profiling.TypeCPU {
		multiplier = int64(1000/p.config.Freq) * int64(time.Millisecond)
	}

	return func(output profile.SampleOutput) error {
		return enqueueFolded(
			output,
			p.labels[output.PID],
			multiplier,
			profiling.LanguageJava,
			enqueue,
		)
	}
}

func prepareJavaSampling(cfg *Config) ([]int, map[int][]types.ProfileLabel, error) {
	if err := toolpath.Validate(profiling.LanguageJava, cfg.ToolDir); err != nil {
		return nil, nil, err
	}

	pids := cfg.PIDs
	if len(pids) == 0 {
		var err error
		pids, err = resolveJavaPIDs(
			cfg.ExecPath,
			cfg.ContainerID,
		)
		if err != nil {
			return nil, nil, err
		}
	}

	if err := validateResolvedPIDs("Java", pids); err != nil {
		return nil, nil, err
	}

	if len(cfg.PIDs) > 0 {
		if err := validateProcessExecutables("Java", "java", pids); err != nil {
			return nil, nil, err
		}

		if err := validateExpectedExecPath(pids, cfg.ExecPath); err != nil {
			return nil, nil, err
		}
	}

	if err := validateMaxProfilerProcesses("Java", pids, cfg.MaxProfilerProcesses); err != nil {
		return nil, nil, err
	}

	labelsByPID := make(map[int][]types.ProfileLabel, len(pids))
	for _, pid := range pids {
		labels, err := readProcessLabels(pid)
		if err != nil {
			return nil, nil, fmt.Errorf("read Java process %d metadata: %w", pid, err)
		}

		entry, err := javaProcessName(pid)
		if err != nil {
			return nil, nil, err
		}

		labels = append(labels, types.ProfileStringLabel("java_entry", entry))
		labelsByPID[pid] = labels
	}

	return pids, labelsByPID, nil
}

func javaAggregationInterval(cfg *Config) time.Duration {
	interval := cfg.AggregationInterval
	if interval <= 0 {
		return 10 * time.Second
	}

	return interval
}
