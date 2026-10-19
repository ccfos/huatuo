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
	"errors"
	"slices"
	"testing"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/bpf"
	bpfabi "github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/symbol"
)

// mockBPF satisfies bpf.BPF for tests by embedding the interface and overriding
// the map access methods; all other methods panic if called.
type mockBPF struct {
	bpf.BPF
	items      map[string][]bpf.MapItem // per map name
	stacks     map[uint32][]byte        // stack id -> raw stack bytes
	stackMapID uint32
}

func (m *mockBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	return m.items[name], nil
}

func (m *mockBPF) MapIDByName(name string) uint32 {
	return m.stackMapID
}

func (m *mockBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	id := binary.LittleEndian.Uint32(key)
	val, ok := m.stacks[id]
	if !ok {
		return nil, errors.New("mock: stack not found")
	}

	return val, nil
}

// stackKeyBytes serializes the generated ABI key into the BPF map layout.
func stackKeyBytes(key *bpfabi.IrqtracingStackKey) []byte {
	buf := &bytes.Buffer{}
	_ = binary.Write(buf, binary.LittleEndian, key)

	return buf.Bytes()
}

// mapItem builds a MapItem for a single stack-counts entry.
func mapItem(key *bpfabi.IrqtracingStackKey, counts ...uint64) bpf.MapItem {
	val := &bytes.Buffer{}
	_ = binary.Write(val, binary.LittleEndian, counts)

	return bpf.MapItem{
		Key:   stackKeyBytes(key),
		Value: val.Bytes(),
	}
}

// stackBytes serializes a u64 frame slice into the raw stack_traces value.
func stackBytes(frames ...uint64) []byte {
	var arr [symbol.KsymStackMaxDepth]uint64
	copy(arr[:], frames)
	buf := &bytes.Buffer{}
	_ = binary.Write(buf, binary.LittleEndian, &arr)

	return buf.Bytes()
}

// TestEnqueueStackMapFrameCount guards the stack frame resolution: only the
// frames stored in the stack_traces map are resolved (zero-filled tail is
// dropped), stack id 0 is a valid id, and a stack id without a map entry is
// an error instead of a silently stackless entry.
func TestEnqueueStackMapFrameCount(t *testing.T) {
	t.Run("resolves ustack and kstack frames", func(t *testing.T) {
		key := &bpfabi.IrqtracingStackKey{
			PID:      123,
			Vec:      3,
			UstackID: 1,
			KstackID: 2,
		}
		b := &mockBPF{
			items: map[string][]bpf.MapItem{"source_counts": {mapItem(key, 1)}},
			stacks: map[uint32][]byte{
				1: stackBytes(0xaaaa0001, 0xaaaa0002),
				2: stackBytes(0xbbbb0001, 0xbbbb0002, 0xbbbb0003),
			},
			stackMapID: 99,
		}
		items, err := collectStackSamples(b)
		assert.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, 5, len(items[0].UserFrames)+len(items[0].KernelFrames))
	})

	t.Run("zero stack id is a valid id", func(t *testing.T) {
		key := &bpfabi.IrqtracingStackKey{
			PID:      123,
			Vec:      3,
			UstackID: 0,
			KstackID: 1,
		}
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{"source_counts": {mapItem(key, 1)}},
			stacks:     map[uint32][]byte{0: stackBytes(0xaaaa0001), 1: stackBytes(0xbbbb0001)},
			stackMapID: 99,
		}
		items, err := collectStackSamples(b)
		assert.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, 2, len(items[0].UserFrames)+len(items[0].KernelFrames))
	})

	t.Run("missing stack id propagates an error", func(t *testing.T) {
		key := &bpfabi.IrqtracingStackKey{
			PID:      123,
			Vec:      3,
			UstackID: 7,
			KstackID: 8,
		}
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{"source_counts": {mapItem(key, 1)}},
			stackMapID: 99,
		}
		_, err := collectStackSamples(b)
		assert.Error(t, err)
	})

	t.Run("zero-filled tail is dropped", func(t *testing.T) {
		key := &bpfabi.IrqtracingStackKey{
			PID:      123,
			Vec:      3,
			UstackID: 1,
			KstackID: 2,
		}
		b := &mockBPF{
			items: map[string][]bpf.MapItem{"source_counts": {mapItem(key, 1)}},
			stacks: map[uint32][]byte{
				1: stackBytes(
					0xaaaa0001,
					0xaaaa0002,
					0,
					0xaaaa0004,
				),
				2: stackBytes(0xbbbb0001, 0, 0xbbbb0003),
			},
			stackMapID: 99,
		}
		items, err := collectStackSamples(b)
		assert.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, 3, len(items[0].UserFrames)+len(items[0].KernelFrames))
	})

	t.Run("multiple entries accumulate", func(t *testing.T) {
		key1 := &bpfabi.IrqtracingStackKey{
			PID:      123,
			Vec:      3,
			UstackID: 1,
			KstackID: 2,
		}
		key2 := &bpfabi.IrqtracingStackKey{
			PID:      456,
			Vec:      3,
			UstackID: 1,
			KstackID: 0,
		}
		b := &mockBPF{
			items: map[string][]bpf.MapItem{
				"source_counts": {mapItem(key1, 1), mapItem(key2, 2)},
			},
			stacks: map[uint32][]byte{
				0: stackBytes(),
				1: stackBytes(0xaaaa0001, 0xaaaa0002),
				2: stackBytes(0xbbbb0001, 0xbbbb0002, 0xbbbb0003),
			},
			stackMapID: 99,
		}
		items, err := collectStackSamples(b)
		assert.NoError(t, err)
		assert.Len(t, items, 2)
		assert.Equal(t, 5, len(items[0].UserFrames)+len(items[0].KernelFrames))
		assert.Len(t, items[1].UserFrames, 2)
	})

	t.Run("keeps labels when stack capture fails", func(t *testing.T) {
		key := &bpfabi.IrqtracingStackKey{
			UstackID: uint32(bpfabi.IrqtracingStackIDNone),
			KstackID: uint32(bpfabi.IrqtracingStackIDNone),
		}
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{"source_counts": {mapItem(key, 1, 2)}},
			stackMapID: 99,
		}

		items, err := collectStackSamples(b)
		assert.NoError(t, err)
		require.Len(t, items, 2)
		for i := range items {
			assert.Empty(t, items[i].UserFrames)
			assert.Empty(t, items[i].KernelFrames)
			assert.Contains(t, items[i].Labels, types.ProfileCPULabel(uint32(i)))
			assert.Contains(t, items[i].Labels, types.ProfileStringLabel("irq", "source"))
		}

		assert.Equal(t, int64(3), items[0].Value+items[1].Value)
	})
}

// mapMockBPF serves ReadMap/MapIDByName for simple key -> value tests.
var errMissingKey = errors.New("mock: key does not exist")

// orderMockBPF records the call order of the narrow snapshot interface so
// tests can lock the detach-before-read contract.
type orderMockBPF struct {
	bpf.BPF
	calls     []string
	detachErr error
	ids       map[string]uint32
	vals      map[uint32][]byte
}

func (m *orderMockBPF) Detach() error {
	m.calls = append(m.calls, "detach")
	return m.detachErr
}

func (m *orderMockBPF) MapIDByName(name string) uint32 {
	m.calls = append(m.calls, "mapid:"+name)
	return m.ids[name]
}

func (m *orderMockBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	m.calls = append(m.calls, "read")
	return m.vals[mapID], nil
}

type mapMockBPF struct {
	bpf.BPF
	ids  map[string]uint32
	vals map[uint32]map[uint32][]byte
	errs map[uint32]error
}

func (m *mapMockBPF) MapIDByName(name string) uint32 {
	return m.ids[name]
}

func (m *mapMockBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	if err, ok := m.errs[mapID]; ok {
		return nil, err
	}

	keys, ok := m.vals[mapID]
	if !ok {
		return nil, errMissingKey
	}

	v, ok := keys[binary.LittleEndian.Uint32(key)]
	if !ok {
		return nil, errMissingKey
	}

	return v, nil
}

// TestReadDroppedSamples covers the completeness contract of the drop
// counter: read failures mean the drop count is unknowable and must be an
// error, never a zero that would mark a truncated profile complete.
func TestReadDroppedSamples(t *testing.T) {
	u64Bytes := func(values ...uint64) []byte {
		buf := &bytes.Buffer{}
		_ = binary.Write(buf, binary.LittleEndian, values)

		return buf.Bytes()
	}

	t.Run("sums streams and CPUs", func(t *testing.T) {
		m := &mapMockBPF{
			ids:  map[string]uint32{"dropped_samples": 7},
			vals: map[uint32]map[uint32][]byte{7: {0: u64Bytes(5, 6), 1: u64Bytes(7, 8)}},
		}
		got, err := readDroppedSamples(m)
		assert.NoError(t, err)
		assert.Equal(t, uint64(26), got)
	})

	t.Run("read failure is an error, not zero drops", func(t *testing.T) {
		tests := []struct {
			name string
			m    *mapMockBPF
		}{
			{
				name: "map missing from loaded object",
				m:    &mapMockBPF{ids: map[string]uint32{}},
			},
			{
				name: "key missing",
				m: &mapMockBPF{
					ids:  map[string]uint32{"dropped_samples": 7},
					vals: map[uint32]map[uint32][]byte{7: {0: u64Bytes(5)}},
				},
			},
			{
				name: "backend error",
				m: &mapMockBPF{
					ids:  map[string]uint32{"dropped_samples": 7},
					errs: map[uint32]error{7: errors.New("huatuo: backend error")},
				},
			},
			{
				name: "short value",
				m: &mapMockBPF{
					ids:  map[string]uint32{"dropped_samples": 7},
					vals: map[uint32]map[uint32][]byte{7: {0: {0xff}}},
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := readDroppedSamples(tt.m)
				assert.Error(t, err, "must not report a complete result")
				assert.Zero(t, got)
			})
		}
	})
}

// TestDetachAndReadDroppedSamplesDetachBeforeReads locks the cut-off contract:
// the snapshot helper must detach the probes before any map read, so moving
// the Detach after a read would fail this test.
func TestDetachAndReadDroppedSamplesDetachBeforeReads(t *testing.T) {
	u64Bytes := func(v uint64) []byte {
		buf := &bytes.Buffer{}
		_ = binary.Write(buf, binary.LittleEndian, v)

		return buf.Bytes()
	}

	m := &orderMockBPF{
		ids: map[string]uint32{"dropped_samples": 2},
		vals: map[uint32][]byte{
			// dropped_samples is read twice (source + victim keys); 6+6 = 12.
			2: u64Bytes(6),
		},
	}

	nmissed, err := detachAndReadDroppedSamples(m)
	assert.NoError(t, err)
	assert.Equal(t, uint64(12), nmissed)
	assert.Equal(t, []string{
		"detach",
		"mapid:dropped_samples",
		"read",
		"read",
	}, m.calls)
}

// TestDetachAndReadDroppedSamplesDetachFailureStopsReads locks the failure
// contract: when the detach fails, no map may be read and the error must
// propagate.
func TestDetachAndReadDroppedSamplesDetachFailureStopsReads(t *testing.T) {
	m := &orderMockBPF{
		detachErr: errors.New("detach boom"),
	}

	_, err := detachAndReadDroppedSamples(m)
	assert.Error(t, err)
	assert.Equal(t, []string{"detach"}, m.calls)
}

func TestEnqueueIRQStacks(t *testing.T) {
	t.Run("empty maps submit no samples", func(t *testing.T) {
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{},
			stacks:     map[uint32][]byte{},
			stackMapID: 99,
		}
		var samples []aggregation.Sample
		err := enqueueIRQStacks(
			b,
			func(batch *pipeline.SampleBatch) error { samples = append(samples, batch.Samples...); return nil },
		)
		assert.NoError(t, err)
		assert.Empty(t, samples)
	})

	t.Run("submits source and victim samples", func(t *testing.T) {
		srcKey := &bpfabi.IrqtracingStackKey{
			PID:      11,
			Vec:      3,
			UstackID: 1,
			KstackID: 2,
		}
		victimKey := &bpfabi.IrqtracingStackKey{
			PID:      22,
			Vec:      3,
			UstackID: 0,
			KstackID: 3,
		}
		copy(srcKey.Comm[:], "raiser")
		copy(victimKey.Comm[:], "worker")
		b := &mockBPF{
			items: map[string][]bpf.MapItem{
				"source_counts": {mapItem(srcKey, 1)},
				"victim_counts": {mapItem(victimKey, 2)},
			},
			stacks: map[uint32][]byte{
				0: stackBytes(),
				1: stackBytes(0xaaaa0001, 0xaaaa0002),
				2: stackBytes(0xbbbb0001, 0xbbbb0002, 0xbbbb0003),
				3: stackBytes(0xcccc0001),
			},
			stackMapID: 99,
		}

		var samples []aggregation.Sample
		err := enqueueIRQStacks(
			b,
			func(batch *pipeline.SampleBatch) error { samples = append(samples, batch.Samples...); return nil },
		)
		assert.NoError(t, err)
		require.Len(t, samples, 2)
		assert.Contains(t, samples[0].Labels, types.ProfileStringLabel("irq", "source"))
		assert.Contains(t, samples[0].Labels, types.ProfileStringLabel("comm", "raiser"))
		assert.Contains(t, samples[1].Labels, types.ProfileStringLabel("irq", "victim"))
		assert.Contains(t, samples[1].Labels, types.ProfilePIDLabel(22))
		values := []int64{samples[0].Value, samples[1].Value}
		slices.Sort(values)
		assert.Equal(t, []int64{1, 2}, values)
	})
}

func collectStackSamples(b bpf.BPF) ([]aggregation.Sample, error) {
	var samples []aggregation.Sample
	err := enqueueStackMap(
		b,
		"source_counts",
		"source",
		symbol.NewUsymResolver(),
		func(batch *pipeline.SampleBatch) error { samples = append(samples, batch.Samples...); return nil },
	)

	return samples, err
}
