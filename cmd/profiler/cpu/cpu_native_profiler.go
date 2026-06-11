// Copyright 2026 The HuaTuo Authors
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
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cilium/ebpf"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/command/container"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler/aggregator"
	agghr "huatuo-bamai/internal/profiler/aggregator/handler"
	util "huatuo-bamai/internal/profiler/common"
	pcontext "huatuo-bamai/internal/profiler/context"
	registry "huatuo-bamai/internal/profiler/registry"
	"huatuo-bamai/internal/symbol"
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

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/cpu_native_profiler.c -o cpu_native_profiler.o

//go:embed cpu_native_profiler.o
var nativeCPUProfilerObj []byte

const taskCommLen = 16

var (
	transferCntIdx uint32
	sampleCntAIdx  = 1
	sampleCntBIdx  = 2
)

type cpuEventKey struct {
	Pid        uint32
	Tgid       uint32
	Cpu        uint32
	Comm       [taskCommLen]byte
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

func (p *cpuNativeProfiler) Stop(pctx *pcontext.ProfilerContext, aggr *aggregator.Aggregator) error {
	if pctx.Cancel != nil {
		pctx.Cancel()
	}

	aggr.Stop()

	if p.bpf != nil {
		if err := p.bpf.Close(); err != nil {
			log.P().Infof("Error closing eBPF: %v", err)
		}
	}

	if pctx.Cancel != nil {
		pctx.Cancel()
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

	b, err := bpf.LoadBpfFromBytes("native_cpu_profiler.o", nativeCPUProfilerObj,
		map[string]any{
			"target_css": cssAddr, "target_pid": uint64(pctx.PID),
		})
	if err != nil {
		log.P().Infof("Failed to load eBPF: %v", err)
		return fmt.Errorf("failed to load bpf: %w", err)
	}

	p.bpf = b
	log.P().Infof("eBPF loaded successfully")

	opt := bpf.AttachOption{ProgramName: "perf_event_sw_cpu_clock"}
	opt.PerfEvent.SampleFreq = uint64(pctx.Freq)
	opt.PerfEvent.SamplePeriod = 0
	if err := p.bpf.AttachWithOptions([]bpf.AttachOption{opt}); err != nil {
		if err := p.bpf.Close(); err != nil {
			log.P().Infof("Error closing eBPF: %v", err)
		}

		log.P().Infof("Failed to attach eBPF: %v", err)
		return fmt.Errorf("failed to attach perf event PMU: %w", err)
	}
	log.P().Infof("eBPF attached successfully")
	return nil
}

func (p *cpuNativeProfiler) ReadDataLoop(ctx context.Context, addRecord func(any)) {
	log.P().Infof("Data reading loop started ")
	defer log.P().Infof("Data reading loop ended")

	readerA, err := p.bpf.EventPipeByName(ctx, "profiler_output_a", 4096*257)
	if err != nil {
		log.P().Infof("failed to create readerA: %v", err)
		return
	}
	defer readerA.Close()
	log.P().Infof("ReaderA created successfully")

	readerB, err := p.bpf.EventPipeByName(ctx, "profiler_output_b", 4096*257)
	if err != nil {
		log.P().Infof("failed to create readerB: %v", err)
		return
	}
	defer readerB.Close()
	log.P().Infof("ReaderB created successfully")

	loopCount := 0
	for {
		select {
		case <-ctx.Done():
			log.P().Infof("Context done, exiting read loop")
			return
		default:
			loopCount++
			if loopCount%100 == 0 {
				log.P().Debugf("Data reading loop iteration %d", loopCount)
			}

			var val uint64

			stateMapID := p.bpf.MapIDByName("profiler_state_map")

			transferCntKeyBytes := make([]byte, 4)
			binary.LittleEndian.PutUint32(transferCntKeyBytes, transferCntIdx)
			transferCntValBytes, err := p.bpf.ReadMap(stateMapID, transferCntKeyBytes)
			if err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
				log.P().Infof("map lookup error: %v", err)
			} else if err == nil && len(transferCntValBytes) >= 8 {
				val = binary.LittleEndian.Uint64(transferCntValBytes)
			}

			var sampleCountIdx uint32
			var stackMapID uint32
			var clearStackIds []int32
			reader := readerA

			stackMapID = p.bpf.MapIDByName("stack_map_a")
			sampleCountIdx = uint32(sampleCntAIdx)

			if val%2 == 1 {
				reader = readerB
				stackMapID = p.bpf.MapIDByName("stack_map_b")
				sampleCountIdx = uint32(sampleCntBIdx)
			}

			var evt cpuEventKey
			nfds, err := reader.EpollWait()
			if err != nil {
				if err.Error() == "reader canceled" {
					return
				}
				if !errors.Is(err, os.ErrDeadlineExceeded) {
					log.P().Infof("read error: %v", err)
				}
			}

			val++

			transferCntKeyBytes2 := make([]byte, 4)
			binary.LittleEndian.PutUint32(transferCntKeyBytes2, transferCntIdx)
			transferCntValBytes2 := make([]byte, 8)
			binary.LittleEndian.PutUint64(transferCntValBytes2, val)

			items := []bpf.MapItem{
				{
					Key:   transferCntKeyBytes2,
					Value: transferCntValBytes2,
				},
			}

			if err := p.bpf.WriteMapItems(stateMapID, items); err != nil {
				log.P().Infof("failed to update profiler state: %v", err)
			}

			stackIdStore := make(map[agghr.ProcessIDName]stackTraceID)
			stackCount := make(map[stackTraceID]int)

			if nfds > 0 {
			checkagain:
				resAll, err := reader.ReadAllRings(evt)
				if err != nil {
					if err.Error() == "reader canceled" {
						return
					}
					if !errors.Is(err, os.ErrDeadlineExceeded) {
						log.P().Infof("read error: %v", err)
					}
				}

				if len(resAll) > 0 {
					log.P().Infof("Processing %d stack events", len(resAll))
				}

				for _, r := range resAll {
					evtPtr, ok := r.(*cpuEventKey)
					if !ok {
						continue
					}

					if evtPtr.Kernstack > 0 || evtPtr.Userstack > 0 {
						pair := stackTraceID{kernelID: evtPtr.Kernstack, userID: evtPtr.Userstack}
						stackCount[pair]++
						pidName := agghr.ProcessIDName{Pid: evtPtr.Pid, Name: util.CommToString(evtPtr.Comm)}
						stackIdStore[pidName] = stackTraceID{kernelID: evtPtr.Kernstack, userID: evtPtr.Userstack}
					}
				}

				if len(stackIdStore) > 0 {
					log.P().Infof("Aggregating %d stack entries", len(stackIdStore))
					clearStackIds = aggregateStacksAndStore(p.bpf, stackIdStore, stackMapID, stackCount, addRecord)
				}

				var bpfCount uint64
				keyBytes := make([]byte, 4)
				binary.LittleEndian.PutUint32(keyBytes, sampleCountIdx)

				valBytes, err := p.bpf.ReadMap(stateMapID, keyBytes)
				if err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
					log.P().Infof("map lookup error: %v", err)
				} else if err == nil && len(valBytes) >= 8 {
					bpfCount = binary.LittleEndian.Uint64(valBytes)
				} else if errors.Is(err, ebpf.ErrKeyNotExist) {
					bpfCount = 0
				}

				if bpfCount > uint64(len(resAll)) {
					snfds, err := reader.EpollShortWait()
					if err == nil && snfds > 0 {
						goto checkagain
					}
				}
			}

			var sampleCntVal uint64 = 0
			sampleCntKeyBytes := make([]byte, 4)
			binary.LittleEndian.PutUint32(sampleCntKeyBytes, sampleCountIdx)
			sampleCntValBytes := make([]byte, 8)
			binary.LittleEndian.PutUint64(sampleCntValBytes, sampleCntVal)

			items = []bpf.MapItem{
				{
					Key:   sampleCntKeyBytes,
					Value: sampleCntValBytes,
				},
			}

			err = p.bpf.WriteMapItems(stateMapID, items)
			if err != nil {
				log.P().Infof("failed to update sample count: %v", err)
			}

			if len(clearStackIds) > 0 {
				clearKeys := make([][]byte, 0, len(clearStackIds))

				for _, clearId := range clearStackIds {
					key := make([]byte, 4)
					binary.LittleEndian.PutUint32(key, uint32(clearId))
					clearKeys = append(clearKeys, key)
				}

				if err := p.bpf.DeleteMapItems(stackMapID, clearKeys); err != nil {
					log.P().Infof("clear stack map keys err: %v", err)
				}
			}
		}
	}
}

func aggregateStacksAndStore(
	b bpf.BPF,
	stackIdStore map[agghr.ProcessIDName]stackTraceID,
	stMapID uint32,
	stackCount map[stackTraceID]int,
	addRecord func(any),
) []int32 {
	allStackIDs := make(map[int32]bool)
	for _, v := range stackIdStore {
		if v.kernelID > 0 {
			allStackIDs[v.kernelID] = true
		}
		if v.userID > 0 {
			allStackIDs[v.userID] = true
		}
	}

	stackData, clearStackIds := batchReadStackTraces(b, stMapID, allStackIDs)
	ustackCache := make(map[int32]string)
	kstackCache := make(map[int32]string)

	usym := symbol.NewUsymResolver()

	for k, v := range stackIdStore {
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
					strs := usym.UsymStackStrsReversed(pid, stackTrace[:], len(stackTrace))
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
	return clearStackIds
}

func batchReadStackTraces(b bpf.BPF, stMapID uint32, stackIDs map[int32]bool) (stackData map[int32][127]uint64, clearStackIds []int32) {
	stackData = make(map[int32][127]uint64, len(stackIDs))
	stackIdKeyBuffer := make([]byte, 4)

	for stackID := range stackIDs {
		binary.LittleEndian.PutUint32(stackIdKeyBuffer, uint32(stackID))

		valBytes, err := b.ReadMap(stMapID, stackIdKeyBuffer)
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

		clearStackIds = append(clearStackIds, stackID)
	}

	return stackData, clearStackIds
}
