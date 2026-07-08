// Copyright 2025 The HuaTuo Authors
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
	"strings"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	"huatuo-bamai/internal/profiler/bpfmap"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_physical_usage.c -o $BPF_DIR/native_physical_usage.o
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_virtual_alloc.c -o $BPF_DIR/native_virtual_alloc.o
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/native_physical_alloc.c -o $BPF_DIR/native_physical_alloc.o

const memDrainTick = 100 * time.Millisecond

const (
	modeVirtualAlloc  = "native_virtual_alloc"
	modePhysicalUsage = "native_physical_usage"
	modePhysicalAlloc = "native_physical_alloc"
)

type memNativeProfiler struct {
	bpf bpf.BPF

	internalMode string
	probability  uint
	pageSize     int64
}

type memEvent struct {
	Pid       uint32
	Comm      [bpf.TaskCommLen]byte
	Kernstack int32
	Userstack int32
	// StackMapSel records which A/B stack_map the IDs came from. Required
	// for retained free events whose alloc-time parity may differ from the
	// current parity at free time; kept in the shared event layout.
	StackMapSel uint32
	Value       int64
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
	mode, err := resolveMemMode(pctx.ExtraFlags["mode"])
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

	internalMode, err := resolveMemMode(pctx.ExtraFlags["mode"])
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

	log.Info("starting native mem profiler", "mode", p.internalMode)

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
				"target_pid":    uint32(pid),
				"target_css":    cssAddr,
				"trace_threads": traceThreads,
			},
			AttachOpts: []bpf.AttachOption{
				{ProgramName: "trace_mmap", Symbol: "do_mmap"},
			},
		}, nil
	case modePhysicalUsage:
		return &bpfLoadConfig{
			ObjectFile: "native_physical_usage.o",
			Constants: map[string]any{
				"target_pid":           uint32(pid),
				"target_css":           cssAddr,
				"trace_threads":        traceThreads,
				"sampling_probability": uint8(probability),
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
				"target_pid":           uint32(pid),
				"target_css":           cssAddr,
				"trace_threads":        traceThreads,
				"sampling_probability": uint8(probability),
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

	readerA, err := p.bpf.EventPipeByName(ctx, "profiler_output_a", 4096*257)
	if err != nil {
		return fmt.Errorf("create mem readerA: %w", err)
	}
	defer readerA.Close()

	readerB, err := p.bpf.EventPipeByName(ctx, "profiler_output_b", 4096*257)
	if err != nil {
		return fmt.Errorf("create mem readerB: %w", err)
	}
	defer readerB.Close()

	stateMapID := p.bpf.MapIDByName("profiler_state_map")
	stackMapAID := p.bpf.MapIDByName("stack_map_a")
	stackMapBID := p.bpf.MapIDByName("stack_map_b")

	usym := symbol.NewUsymResolver()

	ticker := time.NewTicker(memDrainTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		if err := p.drainActiveRing(readerA, readerB, stateMapID, stackMapAID, stackMapBID, usym, enqueue); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			log.Warn("drain failed", "error", err)
		}
	}
}

func (p *memNativeProfiler) drainActiveRing(
	readerA, readerB bpf.PerfEventReader,
	stateMapID, stackMapAID, stackMapBID uint32,
	usym *symbol.UsymResolver,
	enqueue func(any),
) error {
	ring, err := advanceSwapParity(p.bpf, readerA, readerB, stateMapID, "stack_map_a", "stack_map_b")
	if err != nil {
		return err
	}

	// Use nested map structure consistent with CPU profiler
	deltaByProc := make(map[processIDName]map[bpfmap.StackTraceID]int64)

	// Track which stack map each stack ID comes from
	kernelIDToSel := make(map[int32]uint32)
	userIDToSel := make(map[int32]uint32)

	// Collect stack IDs for both maps
	idsA := make(map[int32]bool)
	idsB := make(map[int32]bool)

	// Batch-read events until everything the BPF side wrote has been consumed.
	// The kernel may keep writing to the just-frozen ring briefly after the
	// parity flip, so re-check the sample count and keep draining until the
	// number of events read equals the BPF-reported count.
	totalRead := uint64(0)
	for {
		batch, err := ring.reader.ReadBatch(&memEvent{})
		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return err
			}
			log.Warn("read batch failed", "error", err)
			break
		}

		totalRead += uint64(len(batch))

		for _, rec := range batch {
			evt, ok := rec.(*memEvent)
			if !ok {
				continue
			}

			deltaBytes := p.convertValueToBytes(evt.Value)
			if deltaBytes == 0 {
				continue
			}

			proc := processIDName{
				Pid:  evt.Pid,
				Name: procutil.CommToString(evt.Comm),
			}
			ids := bpfmap.StackTraceID{KernelID: evt.Kernstack, UserID: evt.Userstack}

			// Aggregate by process and stack ID (StackMapSel doesn't affect aggregation)
			if deltaByProc[proc] == nil {
				deltaByProc[proc] = make(map[bpfmap.StackTraceID]int64)
			}
			deltaByProc[proc][ids] += deltaBytes

			// Record StackMapSel mapping for stack resolution
			// StackMapSel indicates which stack map contains the actual stack data
			if ids.KernelID > 0 {
				kernelIDToSel[ids.KernelID] = evt.StackMapSel
			}
			if ids.UserID > 0 {
				userIDToSel[ids.UserID] = evt.StackMapSel
			}

			// Collect stack IDs to the appropriate set based on StackMapSel
			targetSet := idsA
			if evt.StackMapSel%2 == 1 {
				targetSet = idsB
			}

			if ids.KernelID > 0 {
				targetSet[ids.KernelID] = true
			}

			if ids.UserID > 0 {
				targetSet[ids.UserID] = true
			}
		}

		log.Debugf("drain batch: read=%d total=%d procs=%d", len(batch), totalRead, len(deltaByProc))

		// An empty batch means the ring is drained for now; avoid spinning
		// even if the BPF count has not been fully matched.
		if len(batch) == 0 {
			break
		}

		bpfCount, err := bpfmap.ReadUint64(p.bpf, stateMapID, ring.sampleCountIdx)
		if err != nil {
			return fmt.Errorf("read sampleCnt: %w", err)
		}

		log.Debugf("drain check: totalRead=%d bpfCount=%d", totalRead, bpfCount)

		if totalRead >= bpfCount {
			break
		}
	}

	log.Debugf("drain done: totalRead=%d procs=%d", totalRead, len(deltaByProc))

	if err := bpfmap.WriteUint64(p.bpf, stateMapID, ring.sampleCountIdx, 0); err != nil {
		log.Warnf("reset sample count: %v", err)
	}

	if len(deltaByProc) > 0 {
		stackDataA := bpfmap.BatchReadStackTraces(p.bpf, stackMapAID, idsA)
		stackDataB := bpfmap.BatchReadStackTraces(p.bpf, stackMapBID, idsB)
		emitDeltas(deltaByProc, stackDataA, stackDataB, kernelIDToSel, userIDToSel, usym, enqueue)
	}

	return nil
}

func emitDeltas(
	deltaByProc map[processIDName]map[bpfmap.StackTraceID]int64,
	stackDataA, stackDataB map[int32][bpfmap.StackTraceLen]uint64,
	kernelIDToSel, userIDToSel map[int32]uint32,
	usym *symbol.UsymResolver,
	enqueue func(any),
) {
	// Pre-allocate caches for stack resolution
	kstackCache := make(map[int32]string)
	ustackCache := make(map[int32]string)

	for proc, stacks := range deltaByProc {
		for stackID, delta := range stacks {
			if delta == 0 {
				continue
			}

			// Determine which stack data to use based on StackMapSel mapping
			kernelStackData := stackDataA
			if sel, ok := kernelIDToSel[stackID.KernelID]; ok && sel%2 == 1 {
				kernelStackData = stackDataB
			}

			userStackData := stackDataA
			if sel, ok := userIDToSel[stackID.UserID]; ok && sel%2 == 1 {
				userStackData = stackDataB
			}

			// Resolve kernel stack
			if stackID.KernelID > 0 {
				if _, ok := kstackCache[stackID.KernelID]; !ok {
					if trace, exists := kernelStackData[stackID.KernelID]; exists {
						strs := symbol.KsymStackStrsReversed(trace[:], len(trace))
						kstackCache[stackID.KernelID] = strings.Join(strs, ";") + ";"
					}
				}
			}

			// Resolve user stack
			if stackID.UserID > 0 {
				if _, ok := ustackCache[stackID.UserID]; !ok {
					if trace, exists := userStackData[stackID.UserID]; exists {
						strs := usym.UsymStackStrs(proc.Pid, trace[:], len(trace))
						ustackCache[stackID.UserID] = strings.Join(strs, ";") + ";"
					}
				}
			}

			rec := &stackEntry{
				Proc:    &processIDName{Pid: proc.Pid, Name: proc.Name},
				User:    ustackCache[stackID.UserID],
				Kernel:  kstackCache[stackID.KernelID],
				Samples: delta,
			}

			enqueue(rec)
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
