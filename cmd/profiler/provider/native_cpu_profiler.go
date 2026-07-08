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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cilium/ebpf"

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
	Pid        uint32
	Tgid       uint32
	Cpu        uint32
	Comm       [bpf.TaskCommLen]byte
	Kernstack  int32
	Userstack  int32
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
	ringCtx, err := newRingBufferContext(p.bpf, ctx, 4096*257)
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

		if err := p.drainActiveRing(ringCtx, enqueue); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			log.Warnf("drain: %v", err)
		}
	}
}

func (p *cpuNativeProfiler) drainActiveRing(ringCtx *ringBufferContext, enqueue func(any)) error {
	ring, err := ringCtx.advanceSwapParity()
	if err != nil {
		return err
	}

	stackCountsByProc := make(map[processIDName]map[bpfmap.StackTraceID]int)

	// Batch-read events until everything the BPF side wrote has been consumed.
	// The kernel may keep writing to the just-frozen ring briefly after the
	// parity flip, so re-check the sample count and keep draining until the
	// number of events read equals the BPF-reported count.
	totalRead := uint64(0)
	for {
		batch, err := ring.reader.ReadBatch(&cpuEventKey{})
		if err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return err
			}
			log.Warnf("read batch: %v", err)
			break
		}

		totalRead += uint64(len(batch))

		for _, rec := range batch {
			evt, ok := rec.(*cpuEventKey)
			if !ok {
				continue
			}

			if evt.Kernstack <= 0 && evt.Userstack <= 0 {
				continue
			}

			pair := bpfmap.StackTraceID{KernelID: evt.Kernstack, UserID: evt.Userstack}
			pidName := processIDName{Pid: evt.Pid, Name: procutil.CommToString(evt.Comm)}

			if stackCountsByProc[pidName] == nil {
				stackCountsByProc[pidName] = make(map[bpfmap.StackTraceID]int)
			}
			stackCountsByProc[pidName][pair]++
		}

		log.Debugf("drain batch: read=%d total=%d procs=%d", len(batch), totalRead, len(stackCountsByProc))

		// An empty batch means the ring is drained for now; avoid spinning
		// even if the BPF count has not been fully matched.
		if len(batch) == 0 {
			break
		}

		bpfCount, err := bpfmap.ReadUint64(ringCtx.bpf, ringCtx.transferStateMapID, ring.sampleCountIdx)
		if err != nil {
			return fmt.Errorf("read sampleCnt: %w", err)
		}

		log.Debugf("drain check: totalRead=%d bpfCount=%d", totalRead, bpfCount)

		if totalRead >= bpfCount {
			break
		}
	}

	log.Debugf("drain done: totalRead=%d procs=%d", totalRead, len(stackCountsByProc))

	if err := bpfmap.WriteUint64(ringCtx.bpf, ringCtx.transferStateMapID, ring.sampleCountIdx, 0); err != nil {
		log.Warnf("reset sample count: %v", err)
	}

	if len(stackCountsByProc) > 0 {
		var deleteKeys [][]byte
		aggregateStacksAndStore(ringCtx.bpf, stackCountsByProc, ring.stackMapID, enqueue, &deleteKeys)

		if err := ringCtx.bpf.DeleteMapItems(ring.stackMapID, deleteKeys); err != nil {
			log.Warnf("clear stack map: %v", err)
		}
	}

	return nil
}

func aggregateStacksAndStore(
	b bpf.BPF,
	stackCountsByProc map[processIDName]map[bpfmap.StackTraceID]int,
	stackMapID uint32,
	enqueue func(any),
	deleteKeys *[][]byte,
) {
	kstackCache := make(map[int32]string)
	ustackCache := make(map[int32]string)
	usym := symbol.NewUsymResolver()

	var records int
	for pidName, stacks := range stackCountsByProc {
		for stackID, count := range stacks {
			if stackID.KernelID > 0 {
				if _, ok := kstackCache[stackID.KernelID]; !ok {
					kstackCache[stackID.KernelID] = resolveKstack(b, stackMapID, stackID.KernelID, deleteKeys)
				}
			}
			if stackID.UserID > 0 {
				if _, ok := ustackCache[stackID.UserID]; !ok {
					ustackCache[stackID.UserID] = resolveUstack(b, stackMapID, stackID.UserID, pidName.Pid, usym, deleteKeys)
				}
			}

			record := &stackEntry{
				Proc:    &processIDName{Pid: pidName.Pid, Name: pidName.Name},
				User:    ustackCache[stackID.UserID],
				Kernel:  kstackCache[stackID.KernelID],
				Samples: int64(count),
			}

			enqueue(record)
			records++
		}
	}

	log.Debugf("aggregate: procs=%d kstacks=%d ustacks=%d records=%d", len(stackCountsByProc), len(kstackCache), len(ustackCache), records)
}

func resolveKstack(b bpf.BPF, mapID uint32, kernelID int32, deleteKeys *[][]byte) string {
	trace, ok := readAndMarkStackTrace(b, mapID, kernelID, deleteKeys)
	if !ok {
		return ""
	}
	return strings.Join(symbol.KsymStackStrsReversed(trace[:], len(trace)), ";") + ";"
}

func resolveUstack(b bpf.BPF, mapID uint32, userID int32, pid uint32, usym *symbol.UsymResolver, deleteKeys *[][]byte) string {
	trace, ok := readAndMarkStackTrace(b, mapID, userID, deleteKeys)
	if !ok {
		return ""
	}
	return strings.Join(usym.UsymStackStrsReversed(pid, trace[:], len(trace)), ";") + ";"
}

func readAndMarkStackTrace(b bpf.BPF, mapID uint32, id int32, deleteKeys *[][]byte) ([bpfmap.StackTraceLen]uint64, bool) {
	keyBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(keyBuf, uint32(id))

	*deleteKeys = append(*deleteKeys, keyBuf)

	val, err := b.ReadMap(mapID, keyBuf)
	if err != nil {
		if !errors.Is(err, ebpf.ErrKeyNotExist) {
			log.Warnf("stack map lookup for ID %d: %v", id, err)
		}
		return [bpfmap.StackTraceLen]uint64{}, false
	}

	if len(val) != bpfmap.StackTraceLen*8 {
		return [bpfmap.StackTraceLen]uint64{}, false
	}

	var trace [bpfmap.StackTraceLen]uint64
	reader := bytes.NewReader(val)
	if err := binary.Read(reader, binary.LittleEndian, &trace); err != nil {
		return [bpfmap.StackTraceLen]uint64{}, false
	}

	return trace, true
}