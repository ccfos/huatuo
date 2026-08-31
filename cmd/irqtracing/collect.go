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
	"unsafe"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
)

// irqStackKey mirrors struct stack_key in bpf/irq_tracing.c. Stack frames are not
// stored inline; UstackID/KstackID reference entries in the stack_traces map.
type irqStackKey struct {
	UstackID uint32
	KstackID uint32
	Pid      uint32
	Vec      uint32
	Comm     [bpf.TaskCommLen]byte
}

// irqRqTask mirrors struct rq_task in bpf/irq_tracing.c.
type irqRqTask struct {
	Comm [bpf.TaskCommLen]byte
}

// irqRqVictimWindow mirrors struct rq_victim_window in bpf/irq_tracing.c.
type irqRqVictimWindow struct {
	Ts  uint64
	CPU uint32
	Vec uint32
}

// rqVictim is a runqueue member that was waiting behind ksoftirqd, reported as
// a victim even though its backtrace is unavailable.
type rqVictim struct {
	pid  uint32
	comm string
}

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

// mapSnapshotter is the narrow backend surface used by the post-window
// snapshot readers: the probes must be detached before any read so every
// snapshot describes the same cut-off.
type mapSnapshotter interface {
	Detach() error
	MapIDByName(name string) uint32
	ReadMap(mapID uint32, key []byte) ([]byte, error)
	DumpMapByName(mapName string) ([]bpf.MapItem, error)
}

// ksoftirqdWindowMap is the one-entry ARRAY map recording the last softirq
// serviced by ksoftirqd on the target cpu (bpf/irq_tracing.c).
const ksoftirqdWindowMap = "ksoftirqd_window"

// readKsoftirqdWindow returns whether ksoftirqd serviced any softirq on the
// target cpu during the collection window and the last vector it serviced.
// The probes must already be detached so the entry is a stable snapshot.
func readKsoftirqdWindow(b mapSnapshotter) (bool, uint32, error) {
	mapID := b.MapIDByName(ksoftirqdWindowMap)
	if mapID == 0 {
		return false, 0, fmt.Errorf("map %s not found in loaded object", ksoftirqdWindowMap)
	}
	raw, err := b.ReadMap(mapID, make([]byte, 4))
	if err != nil {
		return false, 0, fmt.Errorf("read %s: %w", ksoftirqdWindowMap, err)
	}
	if len(raw) != int(unsafe.Sizeof(irqRqVictimWindow{})) {
		return false, 0, fmt.Errorf("read %s: value size %d, want %d",
			ksoftirqdWindowMap, len(raw), unsafe.Sizeof(irqRqVictimWindow{}))
	}
	var win irqRqVictimWindow
	if err := binary.Read(bytes.NewReader(raw), binary.LittleEndian, &win); err != nil {
		return false, 0, fmt.Errorf("decode %s: %w", ksoftirqdWindowMap, err)
	}
	return win.Ts != 0, win.Vec, nil
}

// droppedSamplesMap is the ARRAY map counting the samples discarded by each
// stream's first-N budget (0: source, 1: victim) in bpf/irq_tracing.c.
const droppedSamplesMap = "dropped_samples"

// readDroppedSamples sums the drop counts of the source and victim streams.
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
	for _, stream := range []uint32{0, 1} {
		key := make([]byte, 4)
		binary.LittleEndian.PutUint32(key, stream)
		raw, err := b.ReadMap(mapID, key)
		if err != nil {
			return 0, fmt.Errorf("read %s[%d]: %w", droppedSamplesMap, stream, err)
		}
		if len(raw) != 8 {
			return 0, fmt.Errorf("read %s[%d]: value size %d, want 8",
				droppedSamplesMap, stream, len(raw))
		}
		total += binary.LittleEndian.Uint64(raw)
	}
	return total, nil
}

// noStackID marks a stack capture that failed inside the kernel: the BPF
// probe rewrites a negative bpf_get_stackid errno into this sentinel so a
// failure can be told apart from a valid stack id, which starts at 0. The
// value cannot collide with a real id because ids index the stack_traces map
// (0..max_entries-1), far below U32_MAX.
const noStackID = 0xFFFFFFFF

// dumpRqTasks reads the rq_tasks map and returns the runqueue members of the
// target cpu. Read or decode failures are returned as errors instead of being
// dropped: an unreadable runqueue dump must not look like a genuinely empty
// one, or the result would claim to be complete.
func dumpRqTasks(b mapSnapshotter) ([]rqVictim, error) {
	items, err := b.DumpMapByName("rq_tasks")
	if err != nil {
		return nil, fmt.Errorf("dump rq_tasks: %w", err)
	}

	tasks := make([]rqVictim, 0, len(items))
	for _, it := range items {
		var pid uint32
		if err := binary.Read(bytes.NewReader(it.Key), binary.LittleEndian, &pid); err != nil {
			return nil, fmt.Errorf("decode rq_tasks key: %w", err)
		}
		// The idle task (swapper/N, pid == 0) is never a real victim; skip it.
		if pid == 0 {
			continue
		}
		var task irqRqTask
		if err := binary.Read(bytes.NewReader(it.Value), binary.LittleEndian, &task); err != nil {
			return nil, fmt.Errorf("decode rq_tasks value for pid %d: %w", pid, err)
		}
		tasks = append(tasks, rqVictim{pid: pid, comm: bytesutil.ToStr(task.Comm[:])})
	}

	return tasks, nil
}

// readStackTrace resolves a stack id (from bpf_get_stackid) into the raw u64
// frame addresses stored in the stack_traces map. The noStackID sentinel
// means the capture failed inside the kernel and resolves to no frames;
// read or decode failures of a real id are real errors the caller must
// surface, not silently turn into a stackless entry.
func readStackTrace(b bpf.BPF, stackMapID, id uint32) ([symbol.KsymStackMaxDepth]uint64, bool, error) {
	var stack [symbol.KsymStackMaxDepth]uint64
	if id == noStackID {
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

// processStackMap reads a BPF stack-counts map, converts each entry into
// profiler.TreeItem stack frames and appends them to items. labelFn produces
// the per-entry leaf label (e.g. "source[comm,HI]"); rootLabel is the root
// frame name prepended to every stack (e.g. "source" or "victim").
func processStackMap(b bpf.BPF, mapName string, u *symbol.UsymResolver,
	items []*profiler.TreeItem,
	labelFn func(key irqStackKey) string,
	rootLabel string,
) ([]*profiler.TreeItem, error) {
	mapItems, err := b.DumpMapByName(mapName)
	if err != nil {
		return items, err
	}

	stackMapID := b.MapIDByName("stack_traces")

	for _, it := range mapItems {
		var key irqStackKey
		if err := binary.Read(bytes.NewReader(it.Key), binary.LittleEndian, &key); err != nil {
			return items, err
		}
		var count uint64
		if err := binary.Read(bytes.NewReader(it.Value), binary.LittleEndian, &count); err != nil {
			return items, err
		}

		var frames [][]byte

		// Top-down order: root -> per-entry label -> ustack (outermost first)
		// -> kstack (outermost first). UsymStackStrsReversed and
		// KsymStackStrsReversed already return the earliest-called frame first.
		frames = append(frames, []byte(rootLabel), []byte(labelFn(key)))

		if ustack, ok, err := readStackTrace(b, stackMapID, key.UstackID); err != nil {
			return items, err
		} else if ok {
			uf := u.UsymStackStrsReversed(key.Pid, ustack[:], symbol.KsymStackMaxDepth)
			for _, f := range uf {
				if f != "" {
					frames = append(frames, []byte(f))
				}
			}
		}

		if kstack, ok, err := readStackTrace(b, stackMapID, key.KstackID); err != nil {
			return items, err
		} else if ok {
			kf := symbol.KsymStackStrsReversed(kstack[:], symbol.KsymStackMaxDepth)
			for _, f := range kf {
				if f != "" {
					frames = append(frames, []byte(f+"_[k]"))
				}
			}
		}

		items = append(items, &profiler.TreeItem{
			Stack: frames,
			Value: count,
		})
	}
	return items, nil
}

// readPostWindowSnapshot freezes the collection point and reads every
// post-window state map: it detaches the probes first, so the drop counter,
// the ksoftirqd window and the runqueue dump all describe the same cut-off.
func readPostWindowSnapshot(b mapSnapshotter) (rqTasks []rqVictim, ksoftirqdHit bool, ksoftirqdVec uint32, nmissed uint64, err error) {
	if err := b.Detach(); err != nil {
		return nil, false, 0, 0, fmt.Errorf("detach: %w", err)
	}

	// ksoftirqdHit records whether ksoftirqd serviced any softirq on the
	// target cpu during the window; ksoftirqdVec keeps the last serviced
	// vector.
	ksoftirqdHit, ksoftirqdVec, err = readKsoftirqdWindow(b)
	if err != nil {
		return nil, false, 0, 0, fmt.Errorf("read ksoftirqd window: %w", err)
	}

	// A non-zero nmissed marks the profile incomplete; an unreadable drop
	// counter means completeness is unknowable, so fail instead of saving a
	// result that downstream would treat as complete.
	nmissed, err = readDroppedSamples(b)
	if err != nil {
		return nil, false, 0, 0, fmt.Errorf("read dropped samples: %w", err)
	}

	rqTasks, err = dumpRqTasks(b)
	if err != nil {
		return nil, false, 0, 0, fmt.Errorf("dump rq tasks: %w", err)
	}
	return rqTasks, ksoftirqdHit, ksoftirqdVec, nmissed, nil
}

// buildFlameGraph merges the source and victim stack maps into a ProfileData
// tree, prefixing each stack with "source" / "victim" roots.
func buildFlameGraph(b bpf.BPF, rqVictims []rqVictim, rqVec uint32) (*profiler.ProfileData, error) {
	u := symbol.NewUsymResolver()

	var items []*profiler.TreeItem
	var err error

	items, err = processStackMap(b, "source_counts", u, items,
		func(key irqStackKey) string {
			return fmt.Sprintf("source[%s,%s]", bytesutil.ToStr(key.Comm[:]), vecName(key.Vec))
		},
		"source",
	)
	if err != nil {
		return nil, err
	}

	items, err = processStackMap(b, "victim_counts", u, items,
		func(key irqStackKey) string {
			return fmt.Sprintf("victim[%s(%d),%s]",
				bytesutil.ToStr(key.Comm[:]), key.Pid, vecName(key.Vec))
		},
		"victim",
	)
	if err != nil {
		return nil, err
	}

	// rq victims are an approximation: they are dumped once after the whole
	// collection window ends (not at each rq_victim_window event), so any task
	// enqueued during the window without a matching deactivate is counted, and
	// all of them share the last reported softirq vector. See main.go.
	for _, v := range rqVictims {
		label := fmt.Sprintf("victim[%s(%d),%s]", v.comm, v.pid, vecName(rqVec))
		items = append(items, &profiler.TreeItem{
			Stack: [][]byte{
				[]byte("victim"),
				[]byte(label),
				[]byte("[UNKNOWN]"),
				[]byte("[UNKNOWN]_[k]"),
			},
			Value: 1,
		})
	}

	if len(items) == 0 {
		return nil, nil
	}

	return profiler.ParseTree(time.Now(), profiler.ProfileTypeIrqTracingSample,
		items, &profiler.ParseOption{SampleRate: profiler.NoSampleRate})
}
