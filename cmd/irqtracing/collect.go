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
	"math"
	"strconv"
	"strings"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	"github.com/ccfos/huatuo/internal/profiling/bpfmap"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/ccfos/huatuo/internal/bpf"
	bpfabi "github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
)

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
			return 0, fmt.Errorf(
				"read %s[%d]: %w",
				droppedSamplesMap,
				stream,
				err,
			)
		}

		count, err := sumUint64Slots(raw)
		if err != nil {
			return 0, fmt.Errorf(
				"decode %s[%d]: %w",
				droppedSamplesMap,
				stream,
				err,
			)
		}

		if count > math.MaxUint64-total {
			return 0, fmt.Errorf("IRQ dropped sample count overflows uint64")
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
		count := binary.LittleEndian.Uint64(raw)
		if count > math.MaxUint64-total {
			return 0, fmt.Errorf("IRQ per-CPU count overflows uint64")
		}

		total += count
		raw = raw[8:]
	}

	return total, nil
}

// enqueueStackMap retains CPU identity and counts each per-CPU slot once.
// Symbolization is shared across slots, but the immutable frames are never
// changed after being handed to the pipeline.
func enqueueStackMap(
	b bpf.BPF,
	mapName, role string,
	u *symbol.UsymResolver,
	enqueue func(*pipeline.SampleBatch) error,
) error {
	items, err := b.DumpMapByName(mapName)
	if err != nil {
		return err
	}

	stackMapID := b.MapIDByName("stack_traces")
	var builder pipeline.BatchBuilder
	for i := range items {
		item := &items[i]
		if len(item.Key) != bpfabi.IrqtracingStackKeySize {
			return fmt.Errorf("invalid %s key size %d", mapName, len(item.Key))
		}

		var key bpfabi.IrqtracingStackKey
		if err := binary.Read(bytes.NewReader(item.Key), binary.LittleEndian, &key); err != nil {
			return err
		}

		if len(item.Value) == 0 || len(item.Value)%8 != 0 {
			return fmt.Errorf("invalid %s per-CPU value size %d", mapName, len(item.Value))
		}

		var user, kernel []types.ProfileFrame
		if key.UstackID != uint32(bpfabi.IrqtracingStackIDNone) {
			stack, err := bpfmap.ReadStackTrace(b, stackMapID, key.UstackID)
			if err != nil {
				return err
			}

			user = irqFrames(stack[:], u.UsymStackStrsReversed(key.PID, stack[:], len(stack)))
		}

		if key.KstackID != uint32(bpfabi.IrqtracingStackIDNone) {
			stack, err := bpfmap.ReadStackTrace(b, stackMapID, key.KstackID)
			if err != nil {
				return err
			}

			kernel = irqFrames(stack[:], symbol.KsymStackStrsReversed(stack[:], len(stack)))
		}

		for offset := 0; offset < len(item.Value); offset += 8 {
			count := binary.LittleEndian.Uint64(item.Value[offset:])
			if count == 0 {
				continue
			}

			if count > math.MaxInt64 {
				return fmt.Errorf("IRQ %s count exceeds int64", role)
			}

			labels, err := types.NormalizeLabels([]types.ProfileLabel{
				types.ProfilePIDLabel(key.PID), types.ProfileCPULabel(uint32(offset / 8)),
				types.ProfileStringLabel("comm", bytesutil.ToStr(key.Comm[:])),
				types.ProfileStringLabel("irq", role),
				types.ProfileStringLabel("softirq_vector", strconv.FormatUint(uint64(key.Vec), 10)),
			})
			if err != nil {
				return err
			}

			sample := aggregation.Sample{
				UserFrames:   user,
				KernelFrames: kernel,
				Labels:       labels,
				Value:        int64(count),
			}
			if err := builder.Append(&sample, enqueue); err != nil {
				return err
			}
		}
	}

	return builder.Flush(enqueue)
}

func irqFrames(addresses []uint64, names []string) []types.ProfileFrame {
	frames := make([]types.ProfileFrame, len(names))
	for i, name := range names {
		if name == "" || name == "unknown" || strings.HasPrefix(name, "unknown ") || strings.HasPrefix(name, "[unknown]/") {
			name = fmt.Sprintf("0x%x", addresses[len(names)-1-i])
		}

		frames[i] = types.ProfileFrame{Name: name}
	}

	return frames
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

func enqueueIRQStacks(b bpf.BPF, enqueue func(*pipeline.SampleBatch) error) error {
	resolver := symbol.NewUsymResolver()
	if err := enqueueStackMap(
		b,
		"source_counts",
		"source",
		resolver,
		enqueue,
	); err != nil {
		return err
	}

	return enqueueStackMap(
		b,
		"victim_counts",
		"victim",
		resolver,
		enqueue,
	)
}
