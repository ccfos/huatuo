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
	"huatuo-bamai/pkg/profiling"
	"huatuo-bamai/pkg/types"
)

func init() {
	impl := &cpuNativeProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:           profiling.TypeCPU,
		Implementation: profiling.ImplementationNative,
		Description:    "Native CPU profiler using ebpf",
		Impl:           impl,
		NewAggregator:  impl.NewAggregator,
	})
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_cpu_profiler.c -o $BPF_DIR/native_cpu_profiler.o
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_cpu_offcpu_profiler.c -o $BPF_DIR/native_cpu_offcpu_profiler.o

// cpuEventKey is the on-wire/event representation emitted by the BPF program.
type cpuEventKey struct {
	ProfilerEventBase
	Timestamp uint64
	Cpu       uint32
	Pad0      uint32
}

// offCPUEventKey mirrors struct offcpu_event_t. Value in the embedded base is
// the selected blocked or runnable duration in nanoseconds.
type offCPUEventKey struct {
	ProfilerEventBase
	StartNS    uint64
	EndNS      uint64
	CPU        uint32
	ABIVersion uint16
	Kind       uint8
	Flags      uint8
}

type cpuNativeProfiler struct {
	bpf          bpf.BPF
	dbg          *bpf.BpfDbg
	offCPU       bool
	offCPUReader *nativeOffCPUReader
}

func (n *cpuNativeProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newNativeAggregator(pctx)
}

func (p *cpuNativeProfiler) Stop(_ *pcontext.ProfilerContext) error {
	if p.offCPUReader != nil {
		if err := p.offCPUReader.Close(); err != nil {
			log.Warnf("closing off-CPU reader: %v", err)
		}
		p.offCPUReader = nil
	}
	if p.offCPU {
		logNativeOffCPUStats(p.bpf)
	}
	return closeBpfSafe(p.bpf)
}

func (p *cpuNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if err := validateNativePIDs("CPU", pctx.PIDs); err != nil {
		return err
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

	objectName := "native_cpu_profiler.o"
	constants := newNativeBPFConstants(pctx.PID(), cssAddr, pctx.ThreadGroup)
	attachOptions := nativeCPUOnCPUAttachOptions(pctx)
	if pctx.CPUMode == profiling.CPUModeOffCPU {
		objectName = "native_cpu_offcpu_profiler.o"
		constants = newNativeOffCPUBPFConstants(pctx, cssAddr)
		attachOptions = nativeCPUOffCPUAttachOptions()
	}
	p.offCPU = pctx.CPUMode == profiling.CPUModeOffCPU

	b, err := bpf.LoadBpf(objectName, p.dbg.WithBpfDbg(constants))
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}

	p.bpf = b
	if p.offCPU {
		p.offCPUReader, err = newNativeOffCPUReader(p.bpf, pctx.Ctx)
		if err != nil {
			_ = p.bpf.Close()
			return fmt.Errorf("create off-CPU event reader: %w", err)
		}
	}

	if err := p.bpf.AttachWithOptions(attachOptions); err != nil {
		if p.offCPUReader != nil {
			_ = p.offCPUReader.Close()
			p.offCPUReader = nil
		}
		if cerr := p.bpf.Close(); cerr != nil {
			log.Warnf("closing eBPF after attach failure: %v", cerr)
		}

		return fmt.Errorf("failed to attach native CPU %s probes: %w", pctx.CPUMode, err)
	}

	log.Infof("eBPF attached")

	return nil
}

func nativeCPUOnCPUAttachOptions(pctx *pcontext.ProfilerContext) []bpf.AttachOption {
	opt := bpf.AttachOption{ProgramName: "perf_event_sw_cpu_clock"}
	opt.PerfEvent.SampleFreq = uint64(pctx.Freq)
	opt.PerfEvent.CPUIDs = pctx.CPUIDs
	return []bpf.AttachOption{opt}
}

func nativeCPUOffCPUAttachOptions() []bpf.AttachOption {
	return []bpf.AttachOption{
		{ProgramName: "native_cpu_offcpu_switch", Symbol: "sched_switch"},
		{ProgramName: "native_cpu_offcpu_wakeup", Symbol: "sched_wakeup"},
		{ProgramName: "native_cpu_offcpu_wakeup_new", Symbol: "sched_wakeup_new"},
		{ProgramName: "native_cpu_offcpu_exit", Symbol: "sched_process_exit"},
		{ProgramName: "native_cpu_offcpu_free", Symbol: "sched_process_free"},
	}
}

func newNativeOffCPUBPFConstants(pctx *pcontext.ProfilerContext, cssAddr uint64) map[string]any {
	constants := newNativeBPFConstants(pctx.PID(), cssAddr, pctx.ThreadGroup)
	constants["profiler_offcpu_metric"] = offCPUMetricCode(pctx.OffCPUMetric)
	constants["profiler_offcpu_min_ns"] = microsecondsToNanoseconds(pctx.OffCPUMinUS)
	constants["profiler_offcpu_max_ns"] = microsecondsToNanoseconds(pctx.OffCPUMaxUS)
	return constants
}

func offCPUMetricCode(metric profiling.OffCPUMetric) uint32 {
	switch metric {
	case profiling.OffCPUMetricBlocked:
		return 1
	case profiling.OffCPUMetricRunnable:
		return 2
	default:
		return 0
	}
}

func microsecondsToNanoseconds(value uint64) uint64 {
	const nsPerMicrosecond = uint64(time.Microsecond)
	if value > ^uint64(0)/nsPerMicrosecond {
		return ^uint64(0)
	}
	return value * nsPerMicrosecond
}

func (p *cpuNativeProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	if p.offCPU {
		return p.readOffCPUDataLoop(ctx, enqueue)
	}
	log.Infof("data reading loop started")
	defer log.Infof("data reading loop ended")

	stopDbg, err := p.dbg.StartDebugEventLoop(ctx, p.bpf, "dbg_native_cpu_dbg_events")
	if err != nil {
		return fmt.Errorf("start bpf debug loop: %w", err)
	}
	defer stopDbg()

	// Initialize ring buffer context once, reuse throughout the profiling loop
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
			func() any { return &cpuEventKey{} }, nil,
		)
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
