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
	"errors"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	"github.com/ccfos/huatuo/internal/profiling/bpfmap"
	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

type userStackCacheKey struct {
	PID     uint32
	StackID int32
}

func nativeProcessLabels(pid uint32, comm string) []types.ProfileLabel {
	return []types.ProfileLabel{types.ProfileStringLabel("comm", comm), types.ProfilePIDLabel(pid)}
}

// nativeSampleProcessor owns caches only for the lifetime of one drained ring.
type nativeSampleProcessor struct {
	mode        profiling.Mode
	stacks      nativeStackResolver
	kernelCache map[int32][]types.ProfileFrame
	userCache   map[userStackCacheKey][]types.ProfileFrame
}

func (p *nativeSampleProcessor) enqueueSamples(
	events []any,
	convert func(int64) (int64, error),
	enqueue func(*pipeline.SampleBatch) error,
) error {
	var builder pipeline.BatchBuilder
	if p.mode == profiling.ModePhysicalUsage {
		samples, err := compactRetainedEvents(events, convert)
		if err != nil {
			return err
		}

		for i := range samples {
			sample := &samples[i]
			if err := p.appendSymbolizedSample(&builder, &sample.source, nil, sample.valueBytes, enqueue); err != nil {
				return err
			}
		}

		return builder.Flush(enqueue)
	}

	for _, record := range events {
		base, cpu, err := extractNativeEvent(record)
		if err != nil {
			return err
		}

		value, err := convert(base.Value)
		if err != nil {
			return err
		}

		if err := p.appendSymbolizedSample(&builder, base, cpu, value, enqueue); err != nil {
			return err
		}
	}

	return builder.Flush(enqueue)
}

// extractNativeEvent borrows the decoded event. CPU is present only for on-CPU
// samples; nil must not be confused with CPU zero.
func extractNativeEvent(record any) (*abi.ProfilerEventBase, *uint32, error) {
	switch event := record.(type) {
	case *abi.ProfilerEventBase:
		if event != nil {
			return event, nil, nil
		}
	case *abi.ProfilerOnCPUEvent:
		if event != nil {
			return &event.Base, &event.CPU, nil
		}
	}

	return nil, nil, fmt.Errorf("unexpected native perf event %T", record)
}

func (p *nativeSampleProcessor) appendSymbolizedSample(
	builder *pipeline.BatchBuilder,
	base *abi.ProfilerEventBase,
	cpu *uint32,
	value int64,
	enqueue func(*pipeline.SampleBatch) error,
) error {
	pid := uint32(base.PIDTGID >> 32)
	userKey := userStackCacheKey{
		PID:     pid,
		StackID: base.Userstack,
	}
	// Missing IDs preserve the sample weight; malformed data and I/O failures abort collection.
	user, ok := p.userCache[userKey]
	if !ok {
		var err error
		user, err = p.stacks.resolveUserFrames(base.Userstack, pid)
		if err != nil && !errors.Is(err, bpfmap.ErrStackNotFound) {
			return err
		}

		p.userCache[userKey] = user
	}

	kernel, ok := p.kernelCache[base.Kernstack]
	if !ok {
		var err error
		kernel, err = p.stacks.resolveKernelFrames(base.Kernstack)
		if err != nil && !errors.Is(err, bpfmap.ErrStackNotFound) {
			return err
		}

		p.kernelCache[base.Kernstack] = kernel
	}

	labels := nativeProcessLabels(pid, taskCommString(base.Comm))
	if cpu != nil {
		labels = append(labels, types.ProfileCPULabel(*cpu))
	}

	labels, err := types.NormalizeLabels(labels)
	if err != nil {
		return err
	}

	sample := aggregation.Sample{
		UserFrames:   user,
		KernelFrames: kernel,
		Labels:       labels,
		Value:        value,
	}

	return builder.Append(&sample, enqueue)
}
