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
	"os"
	"strconv"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_physical_usage.c -o $BPF_DIR/native_physical_usage.o
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_virtual_alloc.c -o $BPF_DIR/native_virtual_alloc.o
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_physical_alloc.c -o $BPF_DIR/native_physical_alloc.o

const (
	modeVirtualAlloc  = "virtual_alloc"
	modePhysicalUsage = "physical_usage"
	modePhysicalAlloc = "physical_alloc"
)

type memNativeProfiler struct {
	bpf bpf.BPF

	internalMode string
	probability  uint
	pageSize     int64
}

func init() {
	impl := &memNativeProfiler{}
	registry.Register(registry.ProfilerMeta{
		Type:          "mem",
		LangOrImpl:    "native",
		Description:   "Native memory profiler using eBPF (virtual_alloc, physical_alloc, physical_usage modes)",
		Impl:          impl,
		NewAggregator: impl.NewAggregator,
	})
}

// NewAggregator stamps OneShotAgg before construction for retained mode —
// alloc/free deltas must collapse in a single shot, not stream every interval.
func (p *memNativeProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	mode, err := resolveMemMode(pctx.MemoryMode)
	if err != nil {
		return nil, err
	}

	if mode == modePhysicalUsage {
		pctx.IsOneShotAgg = true
	}

	return newNativeAggregator(pctx)
}

func (p *memNativeProfiler) Stop(_ *pcontext.ProfilerContext) error {
	return closeBpfSafe(p.bpf)
}

func (p *memNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	if err := requireRoot(); err != nil {
		return err
	}

	p.pageSize = int64(os.Getpagesize())

	internalMode, err := resolveMemMode(pctx.MemoryMode)
	if err != nil {
		return err
	}

	p.internalMode = internalMode

	probability, err := resolveProbability(pctx.ExtraFlags["probability"], internalMode)
	if err != nil {
		return err
	}

	p.probability = probability

	traceThreads, err := resolveScope(pctx.Scope)
	if err != nil {
		return err
	}

	log.Info("starting native memory profiler mode: ", p.internalMode)

	cssAddr, err := resolveContainerCgroupCss(pctx, subsystem.SubsystemMemory)
	if err != nil {
		return err
	}

	cfg, err := newBpfLoadConfig(p.internalMode, pctx.PID, cssAddr, traceThreads, p.probability)
	if err != nil {
		return err
	}

	dbg := bpf.NewDbg(pctx.LogBpfDebug)

	b, err := bpf.LoadBpf(cfg.ObjectFile, dbg.WithBpfDbg(cfg.Constants))
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}

	if err := b.AttachWithOptions(cfg.AttachOpts); err != nil {
		if cerr := b.Close(); cerr != nil {
			log.Warn("closing eBPF after attach failure", "error", cerr)
		}

		return fmt.Errorf("failed to attach: %w", err)
	}

	p.bpf = b
	log.Info("eBPF attached")

	return nil
}

func resolveMemMode(mode string) (string, error) {
	if mode == "" {
		mode = modePhysicalAlloc
	}

	switch mode {
	case modeVirtualAlloc, modePhysicalUsage, modePhysicalAlloc:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid mode %q", mode)
	}
}

func resolveProbability(probStr, internalMode string) (uint, error) {
	probability := uint64(100)

	if probStr != "" {
		prob, err := strconv.ParseUint(probStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid probability value %q: %w", probStr, err)
		}

		probability = prob
	}

	if (internalMode == modePhysicalUsage || internalMode == modePhysicalAlloc) && (probability < 1 || probability > 100) {
		return 0, fmt.Errorf("probability must be between 1 and 100")
	}

	return uint(probability), nil
}

func resolveScope(scope string) (bool, error) {
	switch scope {
	case "thread", "":
		return false, nil
	case "thread-group":
		return true, nil
	case "process-group":
		return false, fmt.Errorf("scope 'process-group' is not supported by mem profiler")
	default:
		return false, fmt.Errorf("unsupported scope for mem profiler: %q", scope)
	}
}

// bpfLoadConfig holds the configuration needed to load and attach a BPF program.
type bpfLoadConfig struct {
	// ObjectFile is the BPF object file name (e.g., "native_virtual_alloc.o").
	ObjectFile string
	// Constants are the constant values to be substituted in the BPF program.
	Constants map[string]any
	// AttachOpts specifies how to attach the BPF program to kernel hooks.
	AttachOpts []bpf.AttachOption
}

// newBpfLoadConfig creates a BPF load configuration based on the profiler mode.
// It returns the appropriate object file, constants, and attachment options for the given mode.
func newBpfLoadConfig(internalMode string, pid int, cssAddr uint64, traceThreads bool, probability uint) (*bpfLoadConfig, error) {
	switch internalMode {
	case modeVirtualAlloc:
		return &bpfLoadConfig{
			ObjectFile: "native_virtual_alloc.o",
			Constants: map[string]any{
				"profiler_filter_pid":     uint32(pid),
				"profiler_filter_css":     cssAddr,
				"profiler_filter_threads": traceThreads,
			},
			AttachOpts: []bpf.AttachOption{
				{ProgramName: "trace_mmap", Symbol: "do_mmap"},
			},
		}, nil
	case modePhysicalUsage:
		return &bpfLoadConfig{
			ObjectFile: "native_physical_usage.o",
			Constants: map[string]any{
				"profiler_filter_pid":     uint32(pid),
				"profiler_filter_css":     cssAddr,
				"profiler_filter_threads": traceThreads,
				"profiler_sampling_prob":  uint8(probability),
			},
			AttachOpts: []bpf.AttachOption{
				{ProgramName: "trace_page_alloc", Symbol: "page_add_new_anon_rmap"},
				{ProgramName: "trace_page_free", Symbol: "page_remove_rmap"},
			},
		}, nil
	case modePhysicalAlloc:
		return &bpfLoadConfig{
			ObjectFile: "native_physical_alloc.o",
			Constants: map[string]any{
				"profiler_filter_pid":     uint32(pid),
				"profiler_filter_css":     cssAddr,
				"profiler_filter_threads": traceThreads,
				"profiler_sampling_prob":  uint8(probability),
			},
			AttachOpts: []bpf.AttachOption{
				{ProgramName: "trace_page_alloc", Symbol: "page_add_new_anon_rmap"},
			},
		}, nil
	}

	return nil, fmt.Errorf("unsupported mem profiler mode: %q", internalMode)
}

func (p *memNativeProfiler) ReadDataLoop(ctx context.Context, enqueue func(any)) error {
	log.Info("data reading loop started")
	defer log.Info("data reading loop ended")

	// Determine if fallback is needed based on profiling mode
	// Retained mode (physical_usage) needs fallback, others don't
	needsFallback := p.internalMode == modePhysicalUsage

	// Initialize ring buffer context once, reuse throughout the profiling loop
	ringCtx, err := newRingBufferContext(p.bpf, ctx, 4096*257, needsFallback)
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

		// Use unified drainActiveRingBuffer with Memory event factory
		stackCountsByProc, ring, err := ringCtx.drainActiveRingBuffer(
			func() any { return &ProfilerEventBase{} },
			p.convertValueToBytes,
		) // Convert pages to bytes
		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			log.Warn("drain failed", "error", err)
			continue
		}

		if len(stackCountsByProc) > 0 {
			ringCtx.aggregateStacksAndEnqueue(stackCountsByProc, ring, enqueue, p.convertValueToBytes)
		}
	}
}

func (p *memNativeProfiler) convertValueToBytes(v int64) int64 {
	switch p.internalMode {
	case modeVirtualAlloc:
		return v
	case modePhysicalAlloc, modePhysicalUsage:
		return v * p.pageSize * 100 / int64(p.probability)
	}

	log.Warn("unknown mem mode, value treated as zero", "mode", p.internalMode)

	return 0
}
