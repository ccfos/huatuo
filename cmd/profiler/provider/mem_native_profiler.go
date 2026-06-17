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
	"huatuo-bamai/internal/command/container"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	agghr "huatuo-bamai/internal/profiler/aggregator/handler"
	"huatuo-bamai/internal/profiler/bpfmap"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/pkg/types"
)

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/pm_retained2.c -o $BPF_DIR/pm_retained2.o
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/vm_accumulative2.c -o $BPF_DIR/vm_accumulative2.o
//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/pm_accumulative2.c -o $BPF_DIR/pm_accumulative2.o

const memDrainTick = 100 * time.Millisecond

const (
	modeVMAccu     = "vm_accumulative"
	modePMRetained = "pm_retained"
	modePMAccu     = "pm_accumulative"
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
	meta := registry.ProfilerMeta{
		Type:        "mem",
		LangOrImpl:  "native",
		Description: "Native memory profiler using ebpf (vm/pm accumulated & retained)",
		Impl:        newMemNativeProfiler(),
	}

	registry.RegisterProfilerMeta(meta.LangOrImpl, meta.Type, meta)
}

func newMemNativeProfiler() registry.Profiler {
	return &memNativeProfiler{}
}

func (n *memNativeProfiler) NewAggregator(pctx *pcontext.ProfilerContext) *aggregator.Aggregator {
	return agghr.NewNativeAggregator(pctx).Aggregator
}

// Stop profiling, abnormal Stop also goes through here
func (p *memNativeProfiler) Stop(pctx *pcontext.ProfilerContext, aggregator *aggregator.Aggregator) error {
	if pctx.Cancel != nil {
		pctx.Cancel()
	}

	if aggregator != nil {
		aggregator.Stop()
	}

	if p.bpf != nil {
		if err := p.bpf.Close(); err != nil {
			log.P().Infof("Error closing eBPF: %v", err)
		}
	}

	return nil
}

func (p *memNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	p.pageSize = int64(os.Getpagesize())

	internalMode, err := resolveMemMode(pctx.ExtraFlags["mode"])
	if err != nil {
		return err
	}

	p.internalMode = internalMode
	if internalMode == modePMRetained {
		// Retained mode emits one record per (alloc - free) delta, which the
		// aggregator must collapse in a single shot rather than streaming.
		pctx.OneShotAgg = true
	}

	probability, err := resolveProbability(pctx.ExtraFlags["probability"], internalMode)
	if err != nil {
		return err
	}

	p.probability = probability

	traceThreads, err := resolveScope(pctx.Scope)
	if err != nil {
		return err
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("eBPF features requires root privileges")
	}

	log.P().Infof("starting native mem profiler, mode=%s", p.internalMode)

	cssAddr, err := resolveCgroupCSS(pctx)
	if err != nil {
		return err
	}

	bpfObjName, consts, opts := bpfPlanForMode(p.internalMode, pctx.PID, cssAddr, traceThreads, p.probability)

	b, err := bpf.LoadBpf(bpfObjName, consts)
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}

	if err := b.AttachWithOptions(opts); err != nil {
		if cerr := b.Close(); cerr != nil {
			log.P().Infof("Error closing eBPF: %v", cerr)
		}

		return fmt.Errorf("failed to attach: %w", err)
	}

	p.bpf = b
	log.P().Infof("native mem eBPF loaded & attached successfully")

	return nil
}

func resolveMemMode(mode string) (string, error) {
	if mode == "" {
		mode = "native_physical_alloc"
	}

	switch mode {
	case "native_virtual_alloc":
		return modeVMAccu, nil
	case "native_physical_usage":
		return modePMRetained, nil
	case "native_physical_alloc":
		return modePMAccu, nil
	default:
		return "", fmt.Errorf("invalid mode %s", mode)
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

	if (internalMode == modePMRetained || internalMode == modePMAccu) && (probability < 1 || probability > 100) {
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
		return false, fmt.Errorf("unsupported scope for mem profiler: %s", scope)
	}
}

func resolveCgroupCSS(pctx *pcontext.ProfilerContext) (uint64, error) {
	if pctx.ContainerID == "" {
		return 0, nil
	}

	c, err := container.GetContainerByID(pctx.ServerAddress, pctx.ContainerID)
	if err != nil {
		return 0, err
	}

	if c == nil {
		return 0, fmt.Errorf("container %s not found", pctx.ContainerID)
	}

	return c.CgroupCss["memory"], nil
}

func bpfPlanForMode(internalMode string, pid int, cssAddr uint64, traceThreads bool, probability uint) (string, map[string]any, []bpf.AttachOption) {
	switch internalMode {
	case modeVMAccu:
		return "vm_accumulative2.o",
			map[string]any{
				"target_pid":    uint32(pid),
				"target_css":    cssAddr,
				"trace_threads": traceThreads,
			},
			[]bpf.AttachOption{
				{ProgramName: "trace_mmap", Symbol: "do_mmap"},
			}
	case modePMRetained:
		return "pm_retained2.o",
			map[string]any{
				"target_pid":           uint32(pid),
				"target_css":           cssAddr,
				"trace_threads":        traceThreads,
				"sampling_probability": uint8(probability),
			},
			[]bpf.AttachOption{
				{ProgramName: "trace_page_alloc", Symbol: "page_add_new_anon_rmap"},
				{ProgramName: "trace_page_free", Symbol: "page_remove_rmap"},
			}
	case modePMAccu:
		return "pm_accumulative2.o",
			map[string]any{
				"target_pid":           uint32(pid),
				"target_css":           cssAddr,
				"trace_threads":        traceThreads,
				"sampling_probability": uint8(probability),
			},
			[]bpf.AttachOption{
				{ProgramName: "trace_page_alloc", Symbol: "page_add_new_anon_rmap"},
			}
	}

	return "", nil, nil
}

func (p *memNativeProfiler) ReadDataLoop(ctx context.Context, addRecord func(any)) {
	log.P().Infof("mem data reading loop started")
	defer log.P().Infof("mem data reading loop ended")

	readerA, err := p.bpf.EventPipeByName(ctx, "profiler_output_a", 4096*257)
	if err != nil {
		log.P().Infof("failed to create mem profiler readerA: %v", err)
		return
	}
	defer readerA.Close()

	readerB, err := p.bpf.EventPipeByName(ctx, "profiler_output_b", 4096*257)
	if err != nil {
		log.P().Infof("failed to create mem profiler readerB: %v", err)
		return
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
			return
		case <-ticker.C:
		}

		if err := p.flipAndDrain(readerA, readerB, stateMapID, stackMapAID, stackMapBID, usym, addRecord); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return
			}

			log.P().Infof("mem drain error: %v", err)
		}
	}
}

// memBatchKey groups events by (process, stack pair, stack-map selector) so
// retained-mode frees that reference the alternate stack_map can be dispatched
// per event without losing the alloc/free delta accumulation.
type memBatchKey struct {
	proc agghr.ProcessIDName
	ids  bpfmap.StackTraceID
	sel  uint32
}

// flipAndDrain advances the BPF write parity and drains the ring that was
// active before the flip. The drain is bounded by sampleCnt set by the BPF
// side, so it never blocks waiting for events that were never written.
func (p *memNativeProfiler) flipAndDrain(
	readerA, readerB bpf.PerfEventReader,
	stateMapID, stackMapAID, stackMapBID uint32,
	usym *symbol.UsymResolver,
	addRecord func(any),
) error {
	val, err := bpfmap.ReadUint64(p.bpf, stateMapID, bpfmap.TransferCntIdx)
	if err != nil {
		return fmt.Errorf("read transferCnt: %w", err)
	}

	reader := readerA
	sampleCountIdx := bpfmap.SampleCntAIdx

	if val%2 == 1 {
		reader = readerB
		sampleCountIdx = bpfmap.SampleCntBIdx
	}

	if err := bpfmap.WriteUint64(p.bpf, stateMapID, bpfmap.TransferCntIdx, val+1); err != nil {
		return fmt.Errorf("write transferCnt: %w", err)
	}

	bpfCount, err := bpfmap.ReadUint64(p.bpf, stateMapID, sampleCountIdx)
	if err != nil {
		return fmt.Errorf("read sampleCnt: %w", err)
	}

	deltaByKey := make(map[memBatchKey]int64)
	idsA := make(map[int32]bool)
	idsB := make(map[int32]bool)

	for i := uint64(0); i < bpfCount; i++ {
		var evt memEvent
		if err := reader.ReadInto(&evt); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return err
			}

			log.P().Infof("mem read error after %d/%d events: %v", i, bpfCount, err)
			break
		}

		deltaBytes := p.convertValueToBytes(evt.Value)
		if deltaBytes == 0 {
			continue
		}

		proc := agghr.ProcessIDName{
			Pid:  evt.Pid,
			Name: procutil.CommToString(evt.Comm),
		}
		ids := bpfmap.StackTraceID{KernelID: evt.Kernstack, UserID: evt.Userstack}
		key := memBatchKey{proc: proc, ids: ids, sel: evt.StackMapSel}
		deltaByKey[key] += deltaBytes

		// In accumulative modes, StackMapSel always matches current parity.
		// In retained mode, alloc events do too, but free events carry
		// alloc-time StackMapSel from page_to_stackid which may differ.
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

	stackDataA := bpfmap.BatchReadStackTraces(p.bpf, stackMapAID, idsA)
	stackDataB := bpfmap.BatchReadStackTraces(p.bpf, stackMapBID, idsB)
	emitDeltas(deltaByKey, stackDataA, stackDataB, usym, addRecord)

	if err := bpfmap.WriteUint64(p.bpf, stateMapID, sampleCountIdx, 0); err != nil {
		log.P().Infof("failed to reset mem sample count: %v", err)
	}

	return nil
}

func emitDeltas(
	deltaByKey map[memBatchKey]int64,
	stackDataA, stackDataB map[int32][bpfmap.StackTraceLen]uint64,
	usym *symbol.UsymResolver,
	addRecord func(any),
) {
	ustackCacheA := make(map[int32]string)
	kstackCacheA := make(map[int32]string)
	ustackCacheB := make(map[int32]string)
	kstackCacheB := make(map[int32]string)

	for k, delta := range deltaByKey {
		if delta == 0 {
			continue
		}

		// StackMapSel picks the correct stack_map to resolve IDs against.
		// For accumulative modes this equals current parity; retained frees
		// may refer to the other map.
		stackData := stackDataA
		ustackCache := ustackCacheA
		kstackCache := kstackCacheA

		if k.sel%2 == 1 {
			stackData = stackDataB
			ustackCache = ustackCacheB
			kstackCache = kstackCacheB
		}

		if k.ids.KernelID > 0 {
			if _, ok := kstackCache[k.ids.KernelID]; !ok {
				if stackTrace, exists := stackData[k.ids.KernelID]; exists {
					kstrs := symbol.KsymStackStrsReversed(stackTrace[:], len(stackTrace))
					kstackCache[k.ids.KernelID] = strings.Join(kstrs, ";") + ";"
				}
			}
		}

		if k.ids.UserID > 0 {
			if _, ok := ustackCache[k.ids.UserID]; !ok {
				if stackTrace, exists := stackData[k.ids.UserID]; exists {
					ustrs := usym.UsymStackStrs(k.proc.Pid, stackTrace[:], len(stackTrace))
					ustackCache[k.ids.UserID] = strings.Join(ustrs, ";") + ";"
				}
			}
		}

		rec := &agghr.StackEntry{
			Proc: &agghr.ProcessIDName{
				Pid:  k.proc.Pid,
				Name: k.proc.Name,
			},
			User:    ustackCache[k.ids.UserID],
			Kernel:  kstackCache[k.ids.KernelID],
			Samples: delta,
		}

		addRecord(rec)
	}
}

func (p *memNativeProfiler) convertValueToBytes(v int64) int64 {
	switch p.internalMode {
	case modeVMAccu:
		return v
	case modePMAccu, modePMRetained:
		// Pages → bytes, scaled up by inverse sampling probability.
		return v * p.pageSize * 100 / int64(p.probability)
	default:
		return 0
	}
}
