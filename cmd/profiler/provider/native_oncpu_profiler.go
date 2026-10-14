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

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"

	"golang.org/x/sys/unix"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_oncpu_profiler.c -o $BPF_DIR/native_oncpu_profiler.o

type cpuNativeProfiler struct {
	config             Config
	bpf                bpf.BPF
	ringCtx            *ringBufferContext
	dbg                *bpf.BpfDbg
	offCPUMode         bool
	offCPUStatsEnabled bool
}

func (p *cpuNativeProfiler) Stop(_ context.Context) error {
	if p.offCPUStatsEnabled {
		logOffCPUBPFStats(p.bpf)
	}
	if p.ringCtx != nil {
		p.ringCtx.Close()
		p.ringCtx = nil
	}

	err := closeBPF(p.bpf)
	p.bpf = nil
	return err
}

func (p *cpuNativeProfiler) Start(ctx context.Context) error {
	cfg := &p.config
	if err := validateNativePIDs("CPU", cfg.PIDs); err != nil {
		return err
	}

	var offCPU bool
	switch cfg.Mode {
	case profiling.ModeOnCPU:
	case profiling.ModeOffCPU:
		offCPU = true
	default:
		return fmt.Errorf("start native CPU profiler: unsupported mode %q", cfg.Mode)
	}

	if err := requireRoot(); err != nil {
		return err
	}

	log.Infof("starting native CPU profiler: mode=%s", cfg.Mode)

	cssAddr, err := resolveContainerCgroupCss(ctx, cfg, subsystem.SubsystemCPU)
	if err != nil {
		return err
	}

	var objectName string
	var constants map[string]any
	if offCPU {
		objectName = "native_offcpu_profiler.o"
		constants = newNativeOffCPUBPFConstants(cfg, cssAddr)
	} else {
		objectName = "native_oncpu_profiler.o"
		constants = newNativeBPFConstants(cfg.PID(), cssAddr, cfg.ThreadGroup)
	}

	dbg := bpf.NewDbg(cfg.LogBpfDebug)
	b, err := bpf.LoadBPF(objectName, dbg.WithBpfDbg(constants))
	if err != nil {
		return fmt.Errorf("load native CPU %s BPF object %q: %w", cfg.Mode, objectName, err)
	}
	if offCPU {
		if err := configureOffCPUSet(b, cfg.CPUIDs); err != nil {
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

	var ringCtx *ringBufferContext
	if offCPU {
		ringCtx, err = newSingleRingBufferContext(b, ctx, 4096*257)
	} else {
		ringCtx, err = newRingBufferContext(b, ctx, 4096*257, false)
	}
	if err != nil {
		readerErr := fmt.Errorf("create native CPU %s event readers: %w", cfg.Mode, err)
		if closeErr := b.Close(); closeErr != nil {
			return errors.Join(
				readerErr,
				fmt.Errorf("close BPF after reader creation failure: %w", closeErr),
			)
		}
		return readerErr
	}

	var attachErr error
	if offCPU {
		attachErr = b.AttachWithOptions(nativeOffCPUAttachOptions())
	} else {
		attachErr = attachNativeOnCPU(b.AttachWithOptions, cfg)
	}
	if attachErr != nil {
		ringCtx.Close()
		attachErr = fmt.Errorf("attach native CPU %s probes: %w", cfg.Mode, attachErr)
		if closeErr := b.Close(); closeErr != nil {
			return errors.Join(
				attachErr,
				fmt.Errorf("close BPF after attach failure: %w", closeErr),
			)
		}
		return attachErr
	}

	p.bpf = b
	p.ringCtx = ringCtx
	p.dbg = dbg
	p.offCPUMode = offCPU
	p.offCPUStatsEnabled = offCPU && cfg.OffCPUStatsEnabled
	log.Infof("eBPF attached")

	return nil
}

func attachNativeOnCPU(
	attach func(opts []bpf.AttachOption) error,
	cfg *Config,
) error {
	hardware := nativeOnCPUAttachOptions(
		cfg,
		unix.PERF_TYPE_HARDWARE,
		unix.PERF_COUNT_HW_CPU_CYCLES,
	)
	hardwareErr := attach(hardware)
	if hardwareErr == nil {
		return nil
	}
	if cfg.RequireHardwarePMU {
		return fmt.Errorf("attach required hardware PMU: %w", hardwareErr)
	}

	software := nativeOnCPUAttachOptions(
		cfg,
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
	cfg *Config,
	eventType uint32,
	eventConfig uint64,
) []bpf.AttachOption {
	opt := bpf.AttachOption{ProgramName: "perf_event_sw_cpu_clock"}
	opt.PerfEvent.SampleFreq = uint64(cfg.Freq)
	opt.PerfEvent.SamplePeriod = 0
	opt.PerfEvent.CPUIDs = cfg.CPUIDs
	opt.PerfEvent.Type = eventType
	opt.PerfEvent.Config = eventConfig
	return []bpf.AttachOption{opt}
}

func (p *cpuNativeProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	if p.ringCtx == nil {
		return errors.New("native CPU event readers are not initialized; call Start before ReadDataLoop")
	}

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
	ringCtx := p.ringCtx

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
