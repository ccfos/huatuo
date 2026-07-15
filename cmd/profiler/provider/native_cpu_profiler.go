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
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/pkg/types"
)

func init() {
	impl := &cpuNativeProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:          "cpu",
		LangOrImpl:    "native",
		Description:   "Native CPU profiler using ebpf",
		Impl:          impl,
		NewAggregator: impl.NewAggregator,
	})
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_cpu_profiler.c -o $BPF_DIR/native_cpu_profiler.o

// cpuEventKey is the on-wire/event representation emitted by the BPF program.
type cpuEventKey struct {
	ProfilerEventBase
	Timestamp uint64
	Cpu       uint32
	Pad0      uint32
}

type cpuNativeProfiler struct {
	bpf bpf.BPF
	dbg *bpf.BpfDbg
}

func (n *cpuNativeProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newNativeAggregator(pctx)
}

func (p *cpuNativeProfiler) Stop(_ *pcontext.ProfilerContext) error {
	return closeBpfSafe(p.bpf)
}

func (p *cpuNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if len(pctx.PIDs) > 1 {
		return fmt.Errorf("start native CPU profiler: multiple PIDs are not supported")
	}
	if err := requireRoot(); err != nil {
		return err
	}

	log.Infof("starting native cpu profiler")

	cssAddr, err := resolveContainerCgroupCss(pctx, subsystem.SubsystemCPU)
	if err != nil {
		return err
	}

	p.dbg = bpf.NewDbg(pctx.LogBpfDebug)

	b, err := bpf.LoadBpf("native_cpu_profiler.o", p.dbg.WithBpfDbg(map[string]any{
		"profiler_filter_css": cssAddr,
		"profiler_filter_pid": uint32(pctx.PID()),
	}))
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}

	p.bpf = b

	opt := bpf.AttachOption{ProgramName: "perf_event_sw_cpu_clock"}
	opt.PerfEvent.SampleFreq = uint64(pctx.Freq)
	opt.PerfEvent.SamplePeriod = 0
	opt.PerfEvent.CPUIDs = pctx.CPUIDs

	if err := p.bpf.AttachWithOptions([]bpf.AttachOption{opt}); err != nil {
		if cerr := p.bpf.Close(); cerr != nil {
			log.Warnf("closing eBPF after attach failure: %v", cerr)
		}

		return fmt.Errorf("failed to attach perf event PMU: %w", err)
	}

	log.Infof("eBPF attached")

	return nil
}

func (p *cpuNativeProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	log.Infof("data reading loop started")
	defer log.Infof("data reading loop ended")

	stopDbg, err := p.dbg.StartDebugEventLoop(ctx, p.bpf, "dbg_native_cpu_dbg_events")
	if err != nil {
		return fmt.Errorf("start bpf debug loop: %w", err)
	}
	defer stopDbg()

	// Initialize ring buffer context once, reuse throughout the profiling loop
	// needsFallback=false for CPU profiler (no dual-stack-map needed)
	ringCtx, err := newRingBufferContext(p.bpf, ctx, 4096*257, false)
	if err != nil {
		return err
	}
	defer ringCtx.Close()

	ticker := time.NewTicker(drainTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		// Use unified drainActiveRingBuffer with CPU event factory
		stackCountsByProc, ring, err := ringCtx.drainActiveRingBuffer(
			func() any { return &cpuEventKey{} },
			nil,
		) // No value conversion needed for CPU profiler
		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			log.Warnf("drain: %v", err)
			continue
		}

		if len(stackCountsByProc) > 0 {
			ringCtx.aggregateStacksAndEnqueue(stackCountsByProc, ring, enqueue, nil)
		}
	}
}
