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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cilium/ebpf"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/command/container"
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
}

func (n *cpuNativeProfiler) NewAggregator(pctx *pcontext.ProfilerContext) (aggregator.Aggregator, error) {
	return newNativeAggregator(pctx)
}

func (p *cpuNativeProfiler) Stop(_ *pcontext.ProfilerContext) error {
	return closeBpfSafe(p.bpf)
}

func (p *cpuNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	log.Infof("starting native cpu profiler")

	var cssAddr uint64
	if containerID := pctx.ContainerID; containerID != "" {
		c, err := container.GetContainerByID(pctx.ServerAddress, containerID)
		if err != nil {
			return err
		}

		cssAddr = c.CgroupCss["cpu"]
	}

	b, err := bpf.LoadBpf("native_cpu_profiler.o", bpf.WithBpfDbg(map[string]any{
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

	stopDbg, err := bpf.StartDebugEventLoop(ctx, p.bpf, "dbg_native_cpu_dbg_events")
	if err != nil {
		return fmt.Errorf("start bpf debug loop: %w", err)
	}
	defer stopDbg()

	readerA, err := p.bpf.EventPipeByName(ctx, "profiler_output_a", 4096*257)
	if err != nil {
		return fmt.Errorf("create readerA: %w", err)
	}
	defer readerA.Close()

	readerB, err := p.bpf.EventPipeByName(ctx, "profiler_output_b", 4096*257)
	if err != nil {
		return fmt.Errorf("create readerB: %w", err)
	}
	defer readerB.Close()

	stateMapID := p.bpf.MapIDByName("profiler_state_map")

	ticker := time.NewTicker(drainTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		if err := p.drainActiveRing(readerA, readerB, stateMapID, enqueue); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			log.Warnf("drain: %v", err)
		}
	}
}

type activeRing struct {
	reader         bpf.PerfEventReader
	stackMapID     uint32
	sampleCountIdx uint32
}

// advanceSwapParity increments the BPF write-parity counter so the kernel
// switches to the other buffer pair, then returns the now-frozen (drainable)
// ring along with the sample-count index used to track how many events the
// BPF side wrote. The caller reads and resets that count while draining.
func (p *cpuNativeProfiler) advanceSwapParity(readerA, readerB bpf.PerfEventReader, stateMapID uint32) (activeRing, error) {
	val, err := bpfmap.ReadUint64(p.bpf, stateMapID, bpfmap.TransferCountIdx)
	if err != nil {
		return activeRing{}, fmt.Errorf("read transferCnt: %w", err)
	}

	var ring activeRing
	if val%2 == 0 {
		ring = activeRing{
			reader:         readerA,
			stackMapID:     p.bpf.MapIDByName("stack_map_a"),
			sampleCountIdx: bpfmap.SampleCountAIdx,
		}
	} else {
		ring = activeRing{
			reader:         readerB,
			stackMapID:     p.bpf.MapIDByName("stack_map_b"),
			sampleCountIdx: bpfmap.SampleCountBIdx,
		}
	}

	if err := bpfmap.WriteUint64(p.bpf, stateMapID, bpfmap.TransferCountIdx, val+1); err != nil {
		return activeRing{}, fmt.Errorf("write transferCnt: %w", err)
	}

	return ring, nil
}

func (p *cpuNativeProfiler) drainActiveRing(readerA, readerB bpf.PerfEventReader, stateMapID uint32, enqueue func(any)) error {
	ring, err := p.advanceSwapParity(readerA, readerB, stateMapID)
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

		bpfCount, err := bpfmap.ReadUint64(p.bpf, stateMapID, ring.sampleCountIdx)
		if err != nil {
			return fmt.Errorf("read sampleCnt: %w", err)
		}

		log.Debugf("drain check: totalRead=%d bpfCount=%d", totalRead, bpfCount)

		if totalRead >= bpfCount {
			break
		}
	}

	log.Debugf("drain done: totalRead=%d procs=%d", totalRead, len(stackCountsByProc))

	if err := bpfmap.WriteUint64(p.bpf, stateMapID, ring.sampleCountIdx, 0); err != nil {
		log.Warnf("reset sample count: %v", err)
	}

	if len(stackCountsByProc) > 0 {
		var deleteKeys [][]byte
		aggregateStacksAndStore(p.bpf, stackCountsByProc, ring.stackMapID, enqueue, &deleteKeys)

		if err := p.bpf.DeleteMapItems(ring.stackMapID, deleteKeys); err != nil {
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
	return strings.Join(usym.UsymStackStrs(pid, trace[:], len(trace)), ";") + ";"
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

func closeBpfSafe(b bpf.BPF) error {
	if b == nil {
		return nil
	}
	if err := b.Close(); err != nil {
		log.Warnf("closing eBPF: %v", err)
	}
	return nil
}
