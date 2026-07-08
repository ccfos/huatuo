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
	"unsafe"

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

// drainTick paces ring-buffer reads. The BPF program writes events to ring A
// or B chosen by transferCnt parity; userspace flips parity each tick, then
// drains the just-frozen ring. ~100ms balances responsiveness and overhead.
const drainTick = 100 * time.Millisecond

// cpuEventKey is the on-wire/event representation emitted by the BPF program.
type cpuEventKey struct {
	ProfilerEventBase
	Tgid       uint32
	Cpu        uint32
	Intpstack  int32
	Flags      uint32
	UprobeAddr uint64
	Timestamp  uint64
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
		"target_css": cssAddr,
		"target_pid": uint64(pctx.PID),
	}))
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}

	p.bpf = b

	opt := bpf.AttachOption{ProgramName: "perf_event_sw_cpu_clock"}
	opt.PerfEvent.SampleFreq = uint64(pctx.Freq)
	opt.PerfEvent.SamplePeriod = 0
	opt.PerfEvent.CPUID = pctx.CPUID

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
		if err := ringCtx.drainActiveRingBuffer(enqueue,
			func() any { return &cpuEventKey{} },
			func(rec any) int64 {
				// Value is now in the embedded base, accessible via pointer conversion
				base := (*ProfilerEventBase)(unsafe.Pointer(&rec))
				return base.Value  // Always 1 for CPU profiler
			},
			nil); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			log.Warnf("drain: %v", err)
		}
	}
}
