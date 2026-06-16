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

package cpu

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
	agghr "huatuo-bamai/internal/profiler/aggregator/handler"
	util "huatuo-bamai/internal/profiler/common"
	pcontext "huatuo-bamai/internal/profiler/context"
	registry "huatuo-bamai/internal/profiler/registry/v2"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/pkg/types"
)

func init() {
	meta := registry.ProfilerMeta{
		Type:        "cpu",
		LangOrImpl:  "native",
		Description: "Native CPU profiler using ebpf",
		Impl:        newCPUNativeProfiler(),
	}

	registry.RegisterProfilerMeta(meta.LangOrImpl, meta.Type, meta)
}

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/cpu_native_profiler2.c -o $BPF_DIR/cpu_native_profiler2.o

// drainTick paces ring-buffer reads. The BPF program writes events to ring A
// or B chosen by transferCnt parity; userspace flips parity each tick, then
// drains the just-frozen ring. ~100ms balances responsiveness and overhead.
const drainTick = 100 * time.Millisecond

const (
	transferCntIdx uint32 = 0
	sampleCntAIdx  uint32 = 1
	sampleCntBIdx  uint32 = 2
)

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

type stackTraceID struct {
	kernelID int32
	userID   int32
}

type cpuNativeProfiler struct {
	bpf bpf.BPF
}

func newCPUNativeProfiler() registry.Profiler {
	return &cpuNativeProfiler{}
}

func (n *cpuNativeProfiler) NewAggregator(pctx *pcontext.ProfilerContext) *aggregator.Aggregator {
	return agghr.NewNativeAggregator(pctx).Aggregator
}

// Stop profiling, abnormal Stop also goes through here
func (p *cpuNativeProfiler) Stop(pctx *pcontext.ProfilerContext, aggregator *aggregator.Aggregator) error {
	if pctx.Cancel != nil {
		pctx.Cancel()
	}

	aggregator.Stop()

	if p.bpf != nil {
		if err := p.bpf.Close(); err != nil {
			log.P().Infof("Error closing eBPF: %v", err)
		}
	}

	return nil
}

func (p *cpuNativeProfiler) Start(pctx *pcontext.ProfilerContext) error {
	log.P().Infof("starting cpu native profiler")

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
			log.P().Infof("Error closing eBPF: %v", cerr)
		}

		return fmt.Errorf("failed to attach perf event PMU: %w", err)
	}

	log.P().Infof("eBPF attached successfully")

	return nil
}

func (p *cpuNativeProfiler) ReadDataLoop(ctx context.Context, addRecord func(any)) {
	log.P().Infof("Data reading loop started")
	defer log.P().Infof("Data reading loop ended")

	readerA, err := p.bpf.EventPipeByName(ctx, "profiler_output_a", 4096*257)
	if err != nil {
		log.P().Infof("failed to create readerA: %v", err)
		return
	}
	defer readerA.Close()

	readerB, err := p.bpf.EventPipeByName(ctx, "profiler_output_b", 4096*257)
	if err != nil {
		log.P().Infof("failed to create readerB: %v", err)
		return
	}
	defer readerB.Close()

	stateMapID := p.bpf.MapIDByName("profiler_state_map")

	ticker := time.NewTicker(drainTick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if err := p.flipAndDrain(readerA, readerB, stateMapID, addRecord); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return
			}

			log.P().Infof("drain error: %v", err)
		}
	}
}

// flipAndDrain advances the BPF write parity and drains the ring that was
// active before the flip. The drain is bounded by sampleCnt (set by the BPF
// side), so it never blocks waiting for events that were never written.
func (p *cpuNativeProfiler) flipAndDrain(readerA, readerB bpf.PerfEventReader, stateMapID uint32, addRecord func(any)) error {
	val, err := readMapUint64(p.bpf, stateMapID, transferCntIdx)
	if err != nil {
		return fmt.Errorf("read transferCnt: %w", err)
	}

	reader := readerA
	stackMapID := p.bpf.MapIDByName("stack_map_a")
	sampleCountIdx := sampleCntAIdx

	if val%2 == 1 {
		reader = readerB
		stackMapID = p.bpf.MapIDByName("stack_map_b")
		sampleCountIdx = sampleCntBIdx
	}

	if err := writeMapUint64(p.bpf, stateMapID, transferCntIdx, val+1); err != nil {
		return fmt.Errorf("write transferCnt: %w", err)
	}

	bpfCount, err := readMapUint64(p.bpf, stateMapID, sampleCountIdx)
	if err != nil {
		return fmt.Errorf("read sampleCnt: %w", err)
	}

	stackIDStore := make(map[agghr.ProcessIDName]stackTraceID)
	stackCount := make(map[stackTraceID]int)

	for i := uint64(0); i < bpfCount; i++ {
		var evt cpuEventKey
		if err := reader.ReadInto(&evt); err != nil {
			if errors.Is(err, types.ErrExitByCancelCtx) {
				return err
			}

			log.P().Infof("read error after %d/%d events: %v", i, bpfCount, err)
			break
		}

		if evt.Kernstack <= 0 && evt.Userstack <= 0 {
			continue
		}

		pair := stackTraceID{kernelID: evt.Kernstack, userID: evt.Userstack}
		stackCount[pair]++
		pidName := agghr.ProcessIDName{Pid: evt.Pid, Name: util.CommToString(evt.Comm)}
		stackIDStore[pidName] = pair
	}

	var clearStackIDs []int32
	if len(stackIDStore) > 0 {
		clearStackIDs = aggregateStacksAndStore(p.bpf, stackIDStore, stackMapID, stackCount, addRecord)
	}

	if err := writeMapUint64(p.bpf, stateMapID, sampleCountIdx, 0); err != nil {
		log.P().Infof("failed to reset sample count: %v", err)
	}

	if len(clearStackIDs) > 0 {
		clearKeys := make([][]byte, 0, len(clearStackIDs))
		for _, id := range clearStackIDs {
			key := make([]byte, 4)
			binary.LittleEndian.PutUint32(key, uint32(id))
			clearKeys = append(clearKeys, key)
		}

		if err := p.bpf.DeleteMapItems(stackMapID, clearKeys); err != nil {
			log.P().Infof("clear stack map keys err: %v", err)
		}
	}

	return nil
}

func readMapUint64(b bpf.BPF, mapID, idx uint32) (uint64, error) {
	key := make([]byte, 4)
	binary.LittleEndian.PutUint32(key, idx)

	val, err := b.ReadMap(mapID, key)
	if err != nil {
		if errors.Is(err, ebpf.ErrKeyNotExist) {
			return 0, nil
		}

		return 0, err
	}

	if len(val) < 8 {
		return 0, nil
	}

	return binary.LittleEndian.Uint64(val), nil
}

func writeMapUint64(b bpf.BPF, mapID, idx uint32, v uint64) error {
	key := make([]byte, 4)
	binary.LittleEndian.PutUint32(key, idx)
	val := make([]byte, 8)
	binary.LittleEndian.PutUint64(val, v)

	return b.WriteMapItems(mapID, []bpf.MapItem{{Key: key, Value: val}})
}

func aggregateStacksAndStore(
	b bpf.BPF,
	stackIDStore map[agghr.ProcessIDName]stackTraceID,
	stMapID uint32,
	stackCount map[stackTraceID]int,
	addRecord func(any),
) []int32 {
	allStackIDs := make(map[int32]bool)
	for _, v := range stackIDStore {
		if v.kernelID > 0 {
			allStackIDs[v.kernelID] = true
		}

		if v.userID > 0 {
			allStackIDs[v.userID] = true
		}
	}

	stackData, clearStackIDs := batchReadStackTraces(b, stMapID, allStackIDs)
	ustackCache := make(map[int32]string)
	kstackCache := make(map[int32]string)

	usym := symbol.NewUsymResolver()

	for k, v := range stackIDStore {
		pid := k.Pid

		if v.kernelID > 0 {
			if _, ok := kstackCache[v.kernelID]; !ok {
				if stackTrace, exists := stackData[v.kernelID]; exists {
					strs := symbol.KsymStackStrsReversed(stackTrace[:], len(stackTrace))
					kstackCache[v.kernelID] = strings.Join(strs, ";") + ";"
				}
			}
		}

		if v.userID > 0 {
			if _, ok := ustackCache[v.userID]; !ok {
				if stackTrace, exists := stackData[v.userID]; exists {
					strs := usym.UsymStackStrs(pid, stackTrace[:], len(stackTrace))
					ustackCache[v.userID] = strings.Join(strs, ";") + ";"
				}
			}
		}

		record := &agghr.StackEntry{
			Proc:    &agghr.ProcessIDName{Pid: pid, Name: k.Name},
			User:    ustackCache[v.userID],
			Kernel:  kstackCache[v.kernelID],
			Samples: int64(stackCount[v]),
		}

		addRecord(record)
	}

	return clearStackIDs
}

func batchReadStackTraces(b bpf.BPF, stMapID uint32, stackIDs map[int32]bool) (map[int32][127]uint64, []int32) {
	stackData := make(map[int32][127]uint64, len(stackIDs))
	clearStackIDs := make([]int32, 0, len(stackIDs))
	stackIDKeyBuffer := make([]byte, 4)

	for stackID := range stackIDs {
		binary.LittleEndian.PutUint32(stackIDKeyBuffer, uint32(stackID))

		valBytes, err := b.ReadMap(stMapID, stackIDKeyBuffer)
		if err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
			log.P().Infof("stack map lookup error for ID %d: %v", stackID, err)
			continue
		}

		if err == nil && len(valBytes) == 127*8 {
			var stackTrace [127]uint64

			reader := bytes.NewReader(valBytes)
			if err := binary.Read(reader, binary.LittleEndian, &stackTrace); err == nil {
				stackData[stackID] = stackTrace
			}
		}

		clearStackIDs = append(clearStackIDs, stackID)
	}

	return stackData, clearStackIDs
}
