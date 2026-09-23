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

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	bpfabi "github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
)

var softirqVecNames = map[uint32]string{
	0: "HI",
	1: "TIMER",
	2: "NET_TX",
	3: "NET_RX",
	4: "BLOCK",
	5: "IRQ_POLL",
	6: "TASKLET",
	7: "SCHED",
	8: "HRTIMER",
	9: "RCU",
}

func vecName(vec uint32) string {
	if name, ok := softirqVecNames[vec]; ok {
		return name
	}
	return "VEC" + strconv.FormatUint(uint64(vec), 10)
}

// mapSnapshotter is the narrow backend surface used to detach the probes and
// read the final drop counters.
type mapSnapshotter interface {
	Detach() error
	MapIDByName(name string) uint32
	ReadMap(mapID uint32, key []byte) ([]byte, error)
}

// droppedSamplesMap is the per-CPU ARRAY map counting samples discarded by each
// stream's first-N budget (0: source, 1: victim) in bpf/irqtracing.c.
const droppedSamplesMap = "dropped_samples"

// readDroppedSamples sums every CPU slot in the source and victim streams.
// A non-zero value means samples were dropped during collection (by the
// first-N budget or by a full counts map) and the flame graph is incomplete.
// A read failure means the drop count is unknowable, not zero: the caller
// must fail loudly instead of saving a result that claims to be complete.
func readDroppedSamples(b mapSnapshotter) (uint64, error) {
	mapID := b.MapIDByName(droppedSamplesMap)
	if mapID == 0 {
		return 0, fmt.Errorf("map %s not found in loaded object", droppedSamplesMap)
	}

	var total uint64
	for stream := bpfabi.IrqtracingStreamSource; stream < bpfabi.IrqtracingStreamMax; stream++ {
		key := make([]byte, 4)
		binary.LittleEndian.PutUint32(key, uint32(stream))
		raw, err := b.ReadMap(mapID, key)
		if err != nil {
			return 0, fmt.Errorf("read %s[%d]: %w", droppedSamplesMap, stream, err)
		}
		count, err := sumUint64Slots(raw)
		if err != nil {
			return 0, fmt.Errorf("decode %s[%d]: %w", droppedSamplesMap, stream, err)
		}
		total += count
	}
	return total, nil
}

func sumUint64Slots(raw []byte) (uint64, error) {
	if len(raw) == 0 || len(raw)%8 != 0 {
		return 0, fmt.Errorf("value size %d is not a non-empty sequence of u64", len(raw))
	}

	var total uint64
	for len(raw) > 0 {
		total += binary.LittleEndian.Uint64(raw)
		raw = raw[8:]
	}
	return total, nil
}

// readStackTrace resolves a stack id (from bpf_get_stackid) into the raw u64
// frame addresses stored in the stack_traces map. The ABI sentinel
// means the capture failed inside the kernel and resolves to no frames;
// read or decode failures of a real id are real errors the caller must
// surface, not silently turn into a stackless entry.
func readStackTrace(b bpf.BPF, stackMapID, id uint32) ([symbol.KsymStackMaxDepth]uint64, bool, error) {
	var stack [symbol.KsymStackMaxDepth]uint64
	if id == uint32(bpfabi.IrqtracingStackIDNone) {
		return stack, false, nil
	}

	keyBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(keyBytes, id)

	valBytes, err := b.ReadMap(stackMapID, keyBytes)
	if err != nil {
		return stack, false, fmt.Errorf("read stack_traces[%d]: %w", id, err)
	}
	if len(valBytes) != symbol.KsymStackMaxDepth*8 {
		return stack, false, fmt.Errorf("read stack_traces[%d]: value size %d, want %d",
			id, len(valBytes), symbol.KsymStackMaxDepth*8)
	}
	if err := binary.Read(bytes.NewReader(valBytes), binary.LittleEndian, &stack); err != nil {
		return stack, false, fmt.Errorf("decode stack_traces[%d]: %w", id, err)
	}
	return stack, true, nil
}

// processStackMap reads a per-CPU BPF stack-counts map, sums its CPU slots,
// converts each entry into profiler.TreeItem stack frames, and appends it to
// items. When provided,
// labelFn produces the per-entry leaf label (e.g. "source[comm,HI]") and
// rootLabel is prepended as the root frame (e.g. "source" or "victim").
func processStackMap(b bpf.BPF, mapName string, u *symbol.UsymResolver,
	items []*profiler.TreeItem,
	labelFn func(key bpfabi.IrqtracingStackKey) string,
	rootLabel string,
) ([]*profiler.TreeItem, error) {
	mapItems, err := b.DumpMapByName(mapName)
	if err != nil {
		return items, err
	}

	stackMapID := b.MapIDByName("stack_traces")

	for _, it := range mapItems {
		var key bpfabi.IrqtracingStackKey
		if err := binary.Read(bytes.NewReader(it.Key), binary.LittleEndian, &key); err != nil {
			return items, err
		}
		count, err := sumUint64Slots(it.Value)
		if err != nil {
			return items, fmt.Errorf("decode %s count: %w", mapName, err)
		}

		// Top-down order: root -> per-entry label -> ustack (outermost first)
		// -> kstack (outermost first). UsymStackStrsReversed and
		// KsymStackStrsReversed already return the earliest-called frame first.
		prefixes := make([]string, 0, 2)
		if rootLabel != "" {
			prefixes = append(prefixes, rootLabel)
		}
		if labelFn != nil {
			if label := labelFn(key); label != "" {
				prefixes = append(prefixes, label)
			}
		}

		var userFrames []string
		if ustack, ok, err := readStackTrace(b, stackMapID, key.UstackID); err != nil {
			return items, err
		} else if ok {
			resolved := u.UsymStackStrsReversed(key.PID, ustack[:], symbol.KsymStackMaxDepth)
			userFrames = resolved[:0]
			for _, f := range resolved {
				if f != "" {
					userFrames = append(userFrames, f)
				}
			}
		}

		var kernelFrames []string
		if kstack, ok, err := readStackTrace(b, stackMapID, key.KstackID); err != nil {
			return items, err
		} else if ok {
			resolved := symbol.KsymStackStrsReversed(kstack[:], symbol.KsymStackMaxDepth)
			kernelFrames = resolved[:0]
			for _, f := range resolved {
				if f != "" {
					kernelFrames = append(kernelFrames, f+"_[k]")
				}
			}
		}

		items = append(items, profiler.BuildTreeItem(prefixes, userFrames, kernelFrames, count))
	}
	return items, nil
}

// detachAndReadDroppedSamples freezes the collection point before reading the
// drop counter so it describes the same cut-off as the stack maps.
func detachAndReadDroppedSamples(b mapSnapshotter) (uint64, error) {
	if err := b.Detach(); err != nil {
		return 0, fmt.Errorf("detach: %w", err)
	}

	// A non-zero nmissed marks the profile incomplete; an unreadable drop
	// counter means completeness is unknowable, so fail instead of saving a
	// result that downstream would treat as complete.
	nmissed, err := readDroppedSamples(b)
	if err != nil {
		return 0, fmt.Errorf("read dropped samples: %w", err)
	}
	return nmissed, nil
}

// buildFlameGraph merges the source and victim stack maps into a ProfileData
// tree, prefixing each stack with "source" / "victim" roots.
func buildFlameGraph(b bpf.BPF, startedAt time.Time) (*profiler.ProfileData, []*profiler.TreeItem, error) {
	u := symbol.NewUsymResolver()

	var items []*profiler.TreeItem
	var err error

	items, err = processStackMap(b, "source_counts", u, items,
		func(key bpfabi.IrqtracingStackKey) string {
			return fmt.Sprintf("source[%s,%s]", bytesutil.ToStr(key.Comm[:]), vecName(key.Vec))
		},
		"source",
	)
	if err != nil {
		return nil, nil, err
	}

	items, err = processStackMap(b, "victim_counts", u, items,
		func(key bpfabi.IrqtracingStackKey) string {
			return fmt.Sprintf("victim[%s(%d),%s]",
				bytesutil.ToStr(key.Comm[:]), key.PID, vecName(key.Vec))
		},
		"victim",
	)
	if err != nil {
		return nil, nil, err
	}

	if len(items) == 0 {
		return nil, nil, nil
	}

	data, err := profiler.ParseTree(startedAt, profiler.ProfileTypeIRQTracingSample,
		items, &profiler.ParseOption{SampleRate: profiler.NoSampleRate})
	if err != nil {
		return nil, nil, err
	}
	return data, items, nil
}
