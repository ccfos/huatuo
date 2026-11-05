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

	"github.com/ccfos/huatuo/internal/profiling/pipeline"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiling/window"
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
	offCPUStatsEnabled bool
}

func (p *cpuNativeProfiler) Finalize(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := p.bpf.Detach(); err != nil {
		return fmt.Errorf("stop native sampling: %w", err)
	}

	if p.config.Mode == profiling.ModeOffCPU {
		return p.drainOffCPUData(ctx, enqueue)
	}

	return p.ringCtx.drainFinalRingBuffers(
		ctx,
		func() any { return &abi.ProfilerOnCPUEvent{} },
		enqueue,
		p.convertCPUValue,
	)
}

func (p *cpuNativeProfiler) Stop() error {
	if p.offCPUStatsEnabled {
		logOffCPUBPFStats(p.bpf)
	}

	var readerErr error
	if p.ringCtx != nil {
		readerErr = p.ringCtx.Close()
		p.ringCtx = nil
	}

	err := closeBPF(p.bpf)
	p.bpf = nil

	return errors.Join(readerErr, err)
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

	target, err := resolveContainerCgroupTarget(ctx, cfg, subsystem.SubsystemCPU)
	if err != nil {
		return err
	}

	var objectName string
	var constants map[string]any
	if offCPU {
		objectName = "native_offcpu_profiler.o"
		constants = newNativeOffCPUBPFConstants(cfg, target)
	} else {
		objectName = "native_oncpu_profiler.o"
		constants = newNativeBPFConstants(cfg.PID(), target, cfg.ThreadGroup)
	}

	dbg := bpf.NewDbg(cfg.LogBpfDebug)
	b, err := bpf.LoadBPF(objectName, dbg.WithBpfDbg(constants))
	if err != nil {
		return fmt.Errorf(
			"load native CPU %s BPF object %q: %w",
			cfg.Mode,
			objectName,
			err,
		)
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
		ringCtx, err = newRingBufferContext(
			b,
			ctx,
			4096*257,
			false,
		)
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
		return errors.Join(
			fmt.Errorf("attach native CPU %s probes: %w", cfg.Mode, attachErr),
			ringCtx.Close(),
			b.Close(),
		)
	}

	p.bpf = b
	p.ringCtx = ringCtx
	p.dbg = dbg
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

func (p *cpuNativeProfiler) ReadDataLoop(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error {
	if p.ringCtx == nil {
		return errors.New("native CPU event readers are not initialized; call Start before ReadDataLoop")
	}

	stopDbg, err := p.dbg.StartDebugEventLoop(ctx, p.bpf, "dbg_native_cpu_dbg_events")
	if err != nil {
		return fmt.Errorf("start native CPU BPF debug loop: %w", err)
	}

	defer stopDbg()

	if p.config.Mode == profiling.ModeOffCPU {
		return p.readOffCPUDataLoop(ctx, enqueue)
	}

	return p.readOnCPUDataLoop(ctx, enqueue)
}

func (p *cpuNativeProfiler) readOnCPUDataLoop(
	ctx context.Context,
	enqueue func(*pipeline.SampleBatch) error,
) error {
	ringCtx := p.ringCtx

	ticker := time.NewTicker(drainInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		err := ringCtx.drainFrozenRingBuffer(func() any { return &abi.ProfilerOnCPUEvent{} }, enqueue, p.convertCPUValue)
		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			return fmt.Errorf("drain native samples: %w", err)
		}
	}
}

func (p *cpuNativeProfiler) convertCPUValue(value int64) (int64, error) {
	if p.config.Freq <= 0 || p.config.Freq > 1000000000 {
		return 0, fmt.Errorf("invalid CPU sample frequency %d", p.config.Freq)
	}

	return window.ScaleValue(value, 1000000000/int64(p.config.Freq))
}
