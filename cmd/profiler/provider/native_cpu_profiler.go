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
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

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

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/cpu_native_profiler2.c -o $BPF_DIR/cpu_native_profiler2.o

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
	log.P().Infof("starting native cpu profiler")

	var cssAddr uint64
	if containerID := pctx.ContainerID; containerID != "" {
		c, err := container.GetContainerByID(pctx.ServerAddress, containerID)
		if err != nil {
			return err
		}

		cssAddr = c.CgroupCss["cpu"]
	}

	b, err := bpf.LoadBpf("cpu_native_profiler2.o", map[string]any{
		"target_css": cssAddr,
		"target_pid": uint64(pctx.PID),
	})
	if err != nil {
		return fmt.Errorf("failed to load bpf: %w", err)
	}

	p.bpf = b

	opt := bpf.AttachOption{ProgramName: "perf_event_sw_cpu_clock"}
	opt.PerfEvent.SampleFreq = uint64(pctx.Freq)
	opt.PerfEvent.SamplePeriod = 0

	if err := p.bpf.AttachWithOptions([]bpf.AttachOption{opt}); err != nil {
		if cerr := p.bpf.Close(); cerr != nil {
			log.P().Warnf("closing eBPF after attach failure: %v", cerr)
		}

		return fmt.Errorf("failed to attach perf event PMU: %w", err)
	}

	log.P().Infof("eBPF attached")

	return nil
}

func (p *cpuNativeProfiler) ReadDataLoop(ctx context.Context, addRecord func(any)) error {
	log.P().Infof("data reading loop started")
	defer log.P().Infof("data reading loop ended")

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

		if err := p.flipAndDrain(readerA, readerB, stateMapID, addRecord); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return nil
			}

			log.P().Warnf("drain: %v", err)
		}
	}
}

// flipAndDrain advances the BPF write parity and drains the ring that was
// active before the flip. The drain is bounded by sampleCnt (set by the BPF
// side), so it never blocks waiting for events that were never written.
func (p *cpuNativeProfiler) flipAndDrain(readerA, readerB bpf.PerfEventReader, stateMapID uint32, addRecord func(any)) error {
	val, err := bpfmap.ReadUint64(p.bpf, stateMapID, bpfmap.TransferCountIdx)
	if err != nil {
		return fmt.Errorf("read transferCnt: %w", err)
	}

	reader := readerA
	stackMapID := p.bpf.MapIDByName("stack_map_a")
	sampleCountIdx := bpfmap.SampleCountAIdx

	if val%2 == 1 {
		reader = readerB
		stackMapID = p.bpf.MapIDByName("stack_map_b")
		sampleCountIdx = bpfmap.SampleCountBIdx
	}

	if err := bpfmap.WriteUint64(p.bpf, stateMapID, bpfmap.TransferCountIdx, val+1); err != nil {
		return fmt.Errorf("write transferCnt: %w", err)
	}

	bpfCount, err := bpfmap.ReadUint64(p.bpf, stateMapID, sampleCountIdx)
	if err != nil {
		return fmt.Errorf("read sampleCnt: %w", err)
	}

	stackIDStore := make(map[processIDName]bpfmap.StackTraceID)
	stackCount := make(map[bpfmap.StackTraceID]int)

	for i := uint64(0); i < bpfCount; i++ {
		var evt cpuEventKey
		if err := reader.ReadInto(&evt); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return err
			}

			log.P().Warnf("read after %d/%d events: %v", i, bpfCount, err)
			break
		}

		if evt.Kernstack <= 0 && evt.Userstack <= 0 {
			continue
		}

		pair := bpfmap.StackTraceID{KernelID: evt.Kernstack, UserID: evt.Userstack}
		stackCount[pair]++
		pidName := processIDName{Pid: evt.Pid, Name: procutil.CommToString(evt.Comm)}
		stackIDStore[pidName] = pair
	}

	if len(stackIDStore) > 0 {
		aggregateStacksAndStore(p.bpf, stackIDStore, stackMapID, stackCount, addRecord)
	}

	if err := bpfmap.WriteUint64(p.bpf, stateMapID, sampleCountIdx, 0); err != nil {
		log.P().Warnf("reset sample count: %v", err)
	}

	if len(stackIDStore) > 0 {
		if err := clearStackMap(p.bpf, stackMapID, stackIDStore); err != nil {
			log.P().Warnf("clear stack map: %v", err)
		}
	}

	return nil
}

func clearStackMap(b bpf.BPF, mapID uint32, stackIDStore map[processIDName]bpfmap.StackTraceID) error {
	seen := make(map[int32]struct{}, len(stackIDStore)*2)
	for _, v := range stackIDStore {
		if v.KernelID > 0 {
			seen[v.KernelID] = struct{}{}
		}

		if v.UserID > 0 {
			seen[v.UserID] = struct{}{}
		}
	}

	if len(seen) == 0 {
		return nil
	}

	n := len(seen)
	buf := make([]byte, 4*n)
	clearKeys := make([][]byte, 0, n)
	i := 0
	for id := range seen {
		binary.LittleEndian.PutUint32(buf[i:i+4], uint32(id))
		clearKeys = append(clearKeys, buf[i:i+4])
		i += 4
	}

	return b.DeleteMapItems(mapID, clearKeys)
}

func aggregateStacksAndStore(
	b bpf.BPF,
	stackIDStore map[processIDName]bpfmap.StackTraceID,
	stMapID uint32,
	stackCount map[bpfmap.StackTraceID]int,
	addRecord func(any),
) {
	allStackIDs := make(map[int32]bool)
	for _, v := range stackIDStore {
		if v.KernelID > 0 {
			allStackIDs[v.KernelID] = true
		}

		if v.UserID > 0 {
			allStackIDs[v.UserID] = true
		}
	}

	stackData := bpfmap.BatchReadStackTraces(b, stMapID, allStackIDs)
	ustackCache := make(map[int32]string)
	kstackCache := make(map[int32]string)
	usym := symbol.NewUsymResolver()

	for k, v := range stackIDStore {
		resolveStackStrs(v, k.Pid, stackData, usym, kstackCache, ustackCache)

		record := &stackEntry{
			Proc:    &processIDName{Pid: k.Pid, Name: k.Name},
			User:    ustackCache[v.UserID],
			Kernel:  kstackCache[v.KernelID],
			Samples: int64(stackCount[v]),
		}

		addRecord(record)
	}
}

// resolveStackStrs populates kernel/user stack string caches for a single
// StackTraceID. Shared by CPU and memory profilers to avoid duplicating the
// symbol-resolution + cache-fill pattern.
func resolveStackStrs(
	ids bpfmap.StackTraceID,
	pid uint32,
	stackData map[int32][bpfmap.StackTraceLen]uint64,
	usym *symbol.UsymResolver,
	kstackCache, ustackCache map[int32]string,
) {
	if ids.KernelID > 0 {
		if _, ok := kstackCache[ids.KernelID]; !ok {
			if trace, exists := stackData[ids.KernelID]; exists {
				strs := symbol.KsymStackStrsReversed(trace[:], len(trace))
				kstackCache[ids.KernelID] = strings.Join(strs, ";") + ";"
			}
		}
	}

	if ids.UserID > 0 {
		if _, ok := ustackCache[ids.UserID]; !ok {
			if trace, exists := stackData[ids.UserID]; exists {
				strs := usym.UsymStackStrs(pid, trace[:], len(trace))
				ustackCache[ids.UserID] = strings.Join(strs, ";") + ";"
			}
		}
	}
}

func closeBpfSafe(b bpf.BPF) error {
	if b == nil {
		return nil
	}
	if err := b.Close(); err != nil {
		log.P().Warnf("closing eBPF: %v", err)
	}
	return nil
}
