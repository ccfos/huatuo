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
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/pkg/profiling"
	"huatuo-bamai/pkg/types"

	"golang.org/x/sys/unix"
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

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_oncpu_profiler.c -o $BPF_DIR/native_oncpu_profiler.o

type cpuNativeProfiler struct {
	bpf                bpf.BPF
	dbg                *bpf.BpfDbg
	offCPUMode         bool
	offCPUStatsEnabled bool
}

func (n *cpuNativeProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newNativeAggregator(pctx)
}

func (p *cpuNativeProfiler) Stop(_ *pcontext.ProfilerContext) error {
	if p.offCPUStatsEnabled {
		logOffCPUBPFStats(p.bpf)
	}
	return closeBPF(p.bpf)
}

func (p *cpuNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if err := validateNativePIDs("CPU", pctx.PIDs); err != nil {
		return err
	}

	var offCPU bool
	switch pctx.CPUMode {
	case profiling.CPUModeOnCPU:
	case profiling.CPUModeOffCPU:
		offCPU = true
	default:
		return fmt.Errorf("start native CPU profiler: unsupported mode %q", pctx.CPUMode)
	}

	if err := requireRoot(); err != nil {
		return err
	}

	log.Infof("starting native CPU profiler: mode=%s", pctx.CPUMode)

	cssAddr, err := resolveContainerCgroupCss(pctx, subsystem.SubsystemCPU)
	if err != nil {
		return err
	}

	var objectName string
	var constants map[string]any
	if offCPU {
		objectName = "native_offcpu_profiler.o"
		constants = newNativeOffCPUBPFConstants(pctx, cssAddr)
	} else {
		objectName = "native_oncpu_profiler.o"
		constants = newNativeBPFConstants(pctx.PID(), cssAddr, pctx.ThreadGroup)
	}

	dbg := bpf.NewDbg(pctx.LogBpfDebug)
	b, err := bpf.LoadBPF(objectName, dbg.WithBpfDbg(constants))
	if err != nil {
		return fmt.Errorf("load native CPU %s BPF object %q: %w", pctx.CPUMode, objectName, err)
	}
	if offCPU {
		if err := configureOffCPUSet(b, pctx.CPUIDs); err != nil {
			configureErr := fmt.Errorf("configure native CPU off-CPU filter: %w", err)
			if closeErr := b.Close(); closeErr != nil {
				return errors.Join(
					configureErr,
					fmt.Errorf("close BPF after configure failure: %w", closeErr),
				)
			}
			return configureErr
		}
	}

	var attachErr error
	if offCPU {
		attachErr = b.AttachWithOptions(nativeOffCPUAttachOptions())
	} else {
		attachErr = attachNativeOnCPU(b.AttachWithOptions, pctx)
	}
	if attachErr != nil {
		attachErr = fmt.Errorf("attach native CPU %s probes: %w", pctx.CPUMode, attachErr)
		if closeErr := b.Close(); closeErr != nil {
			return errors.Join(
				attachErr,
				fmt.Errorf("close BPF after attach failure: %w", closeErr),
			)
		}
		return attachErr
	}

	p.bpf = b
	p.dbg = dbg
	p.offCPUMode = offCPU
	p.offCPUStatsEnabled = offCPU && pctx.OffCPUStatsEnabled
	log.Infof("eBPF attached")

	return nil
}

func attachNativeOnCPU(
	attach func(opts []bpf.AttachOption) error,
	pctx *pcontext.ProfilerContext,
) error {
	hardware := nativeOnCPUAttachOptions(
		pctx,
		unix.PERF_TYPE_HARDWARE,
		unix.PERF_COUNT_HW_CPU_CYCLES,
	)
	hardwareErr := attach(hardware)
	if hardwareErr == nil {
		return nil
	}
	if pctx.RequireHardwarePMU {
		return fmt.Errorf("attach required hardware PMU: %w", hardwareErr)
	}

	software := nativeOnCPUAttachOptions(
		pctx,
		unix.PERF_TYPE_SOFTWARE,
		unix.PERF_COUNT_SW_CPU_CLOCK,
	)
	if softwareErr := attach(software); softwareErr != nil {
		return errors.Join(
			fmt.Errorf("attach hardware PMU: %w", hardwareErr),
			fmt.Errorf("attach software CPU clock: %w", softwareErr),
		)
	}

	log.WithError(hardwareErr).Warn("hardware PMU unavailable; using software CPU clock")
	return nil
}

func nativeOnCPUAttachOptions(
	pctx *pcontext.ProfilerContext,
	eventType uint32,
	eventConfig uint64,
) []bpf.AttachOption {
	opt := bpf.AttachOption{ProgramName: "perf_event_sw_cpu_clock"}
	opt.PerfEvent.SampleFreq = uint64(pctx.Freq)
	opt.PerfEvent.SamplePeriod = 0
	opt.PerfEvent.CPUIDs = pctx.CPUIDs
	opt.PerfEvent.Type = eventType
	opt.PerfEvent.Config = eventConfig
	return []bpf.AttachOption{opt}
}

func (p *cpuNativeProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	log.Info("data reading loop started")
	defer log.Info("data reading loop ended")

	stopDbg, err := p.dbg.StartDebugEventLoop(ctx, p.bpf, "dbg_native_cpu_dbg_events")
	if err != nil {
		return fmt.Errorf("start native CPU BPF debug loop: %w", err)
	}
	defer stopDbg()

	if p.offCPUMode {
		return p.readOffCPUDataLoop(ctx, enqueue)
	}
	return p.readOnCPUDataLoop(ctx, enqueue)
}

func (p *cpuNativeProfiler) readOnCPUDataLoop(ctx context.Context, enqueue func(any)) error {
	// Initialize ring buffer context once, reuse throughout the profiling loop
	ringCtx, err := newRingBufferContext(p.bpf, ctx, 4096*257, false)
	if err != nil {
		return err
	}
	defer ringCtx.Close()

	ticker := time.NewTicker(drainInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		// Use unified drainFrozenRingBuffer with CPU event factory
		sampleCountsByProcess, ring, err := ringCtx.drainFrozenRingBuffer(
			func() any { return &abi.ProfilerOnCPUEvent{} },
		)
		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			log.Warnf("drain: %v", err)
			continue
		}

		if len(sampleCountsByProcess) > 0 {
			ringCtx.aggregateStacksAndEnqueue(sampleCountsByProcess, ring, enqueue, nil)
		}
	}
}
