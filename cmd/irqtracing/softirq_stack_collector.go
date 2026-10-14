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

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/profiler/aggregator"
	"github.com/ccfos/huatuo/internal/profiler/registry"
)

const (
	sourceRateLimitConstant = "source_rate_limit"
	victimRateLimitConstant = "victim_rate_limit"
)

// The CLI validates collection settings before constructing a collector.
type collector struct {
	bpfPath                  string
	cpuId                    int32
	duration                 time.Duration
	maxEventsPerSecondPerCPU uint64
	bpfObj                   bpf.BPF
}

// newIRQProfiler binds one IRQ collection to the common runner.
func newIRQProfiler(c *collector) *registry.ProfilerMeta {
	return &registry.ProfilerMeta{
		ToolName:        irqTracingToolName,
		Description:     "softirq profiling",
		Impl:            c,
		AggregationMode: aggregator.AggregationModeOneShot,
		Aggregator:      &irqAggregator{},
	}
}

func (*collector) ManagesDuration() {}

func (c *collector) Start(_ context.Context) error {
	data, err := os.ReadFile(c.bpfPath)
	if err != nil {
		return fmt.Errorf("read IRQ BPF object: %w", err)
	}
	object, err := bpf.LoadBPFFromBytes(fmt.Sprintf("irqtracing_%d.o", time.Now().UnixNano()), data,
		irqTracingBPFConstants(c.cpuId, c.maxEventsPerSecondPerCPU))
	if err != nil {
		return fmt.Errorf("load IRQ BPF: %w", err)
	}
	if err := object.AttachWithOptions([]bpf.AttachOption{
		{ProgramName: "probe_softirq_raise", Symbol: "irq/softirq_raise"},
		{ProgramName: "probe_softirq_entry", Symbol: "irq/softirq_entry"},
	}); err != nil {
		object.Close()
		return fmt.Errorf("attach IRQ probes: %w", err)
	}
	c.bpfObj = object
	return nil
}

func (c *collector) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	timer := time.NewTimer(c.duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("IRQ collection stopped: %w", ctx.Err())
	case <-timer.C:
	}

	dropped, err := detachAndReadDroppedSamples(c.bpfObj)
	if err != nil {
		return err
	}

	data, err := buildProfile(c.bpfObj)
	if err != nil {
		return fmt.Errorf("build IRQ profile: %w", err)
	}

	enqueue(&softIRQBatch{profile: data, dropped: dropped, targetCPU: c.cpuId})
	return nil
}

func (c *collector) Stop(_ context.Context) error {
	if c.bpfObj != nil {
		c.bpfObj.Close()
		c.bpfObj = nil
	}
	return nil
}

func irqTracingBPFConstants(targetCPU int32, maxEventsPerSecondPerCPU uint64) map[string]any {
	constants := map[string]any{"target_cpu": targetCPU}
	if maxEventsPerSecondPerCPU != 0 {
		constants[sourceRateLimitConstant] = uint32(maxEventsPerSecondPerCPU/2 + maxEventsPerSecondPerCPU%2)
		constants[victimRateLimitConstant] = uint32(maxEventsPerSecondPerCPU / 2)
	}
	return constants
}
