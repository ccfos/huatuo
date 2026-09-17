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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/profiler"
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

// stackKeyBytes serializes an irqStackKey into the byte layout read back from
// the BPF map.
func stackKeyBytes(key *irqStackKey) []byte {
	buf := &bytes.Buffer{}
	_ = binary.Write(buf, binary.LittleEndian, key)
	return buf.Bytes()
}

// mapItem builds a MapItem for a single stack-counts entry.
func mapItem(key *irqStackKey, count uint64) bpf.MapItem {
	val := &bytes.Buffer{}
	_ = binary.Write(val, binary.LittleEndian, &count)
	return bpf.MapItem{Key: stackKeyBytes(key), Value: val.Bytes()}
}

// stackBytes serializes a u64 frame slice into the raw stack_traces value.
func stackBytes(frames ...uint64) []byte {
	var arr [symbol.KsymStackMaxDepth]uint64
	copy(arr[:], frames)
	buf := &bytes.Buffer{}
	_ = binary.Write(buf, binary.LittleEndian, &arr)
	return buf.Bytes()
}

// TestProcessStackMapFrameCount guards the stack frame resolution: only the
// frames stored in the stack_traces map are resolved (zero-filled tail is
// dropped), stack id 0 is a valid id, and a stack id without a map entry is
// an error instead of a silently stackless entry.
func TestProcessStackMapFrameCount(t *testing.T) {
	t.Run("resolves ustack and kstack frames", func(t *testing.T) {
		key := &irqStackKey{Pid: 123, Vec: 3, UstackID: 1, KstackID: 2}
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{"source_counts": {mapItem(key, 1)}},
			stacks:     map[uint32][]byte{1: stackBytes(0xaaaa0001, 0xaaaa0002), 2: stackBytes(0xbbbb0001, 0xbbbb0002, 0xbbbb0003)},
			stackMapID: 99,
		}
		items, err := processStackMap(b, "source_counts", symbol.NewUsymResolver(), nil,
			func(key irqStackKey) string { return "label" },
			"source",
		)
		assert.NoError(t, err)
		requireLen(t, items, 1)
		// root + label + 2 ustack + 3 kstack frames.
		assert.Len(t, items[0].Stack, 7)
	})

	t.Run("zero stack id is a valid id", func(t *testing.T) {
		key := &irqStackKey{Pid: 123, Vec: 3, UstackID: 0, KstackID: 1}
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{"source_counts": {mapItem(key, 1)}},
			stacks:     map[uint32][]byte{0: stackBytes(0xaaaa0001), 1: stackBytes(0xbbbb0001)},
			stackMapID: 99,
		}
		items, err := processStackMap(b, "source_counts", symbol.NewUsymResolver(), nil,
			func(key irqStackKey) string { return "label" },
			"source",
		)
		assert.NoError(t, err)
		requireLen(t, items, 1)
		// root + label + 1 ustack + 1 kstack frames.
		assert.Len(t, items[0].Stack, 4)
	})

	t.Run("missing stack id propagates an error", func(t *testing.T) {
		key := &irqStackKey{Pid: 123, Vec: 3, UstackID: 7, KstackID: 8}
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{"source_counts": {mapItem(key, 1)}},
			stackMapID: 99,
		}
		_, err := processStackMap(b, "source_counts", symbol.NewUsymResolver(), nil,
			func(key irqStackKey) string { return "label" },
			"source",
		)
		assert.Error(t, err)
	})

	t.Run("zero-filled tail is dropped", func(t *testing.T) {
		key := &irqStackKey{Pid: 123, Vec: 3, UstackID: 1, KstackID: 2}
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{"source_counts": {mapItem(key, 1)}},
			stacks:     map[uint32][]byte{1: stackBytes(0xaaaa0001, 0xaaaa0002, 0, 0xaaaa0004), 2: stackBytes(0xbbbb0001, 0, 0xbbbb0003)},
			stackMapID: 99,
		}
		items, err := processStackMap(b, "source_counts", symbol.NewUsymResolver(), nil,
			func(key irqStackKey) string { return "label" },
			"source",
		)
		assert.NoError(t, err)
		requireLen(t, items, 1)
		// Root+label + ustack[2] (frame at 0xaaaa0004 after a zero is dropped
		// by resolveStack stopping at first zero) + kstack[1].
		assert.Len(t, items[0].Stack, 5)
	})

	t.Run("multiple entries accumulate", func(t *testing.T) {
		key1 := &irqStackKey{Pid: 123, Vec: 3, UstackID: 1, KstackID: 2}
		key2 := &irqStackKey{Pid: 456, Vec: 3, UstackID: 1, KstackID: 0}
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
		items, err := processStackMap(b, "source_counts", symbol.NewUsymResolver(), nil,
			func(key irqStackKey) string { return "label" },
			"source",
		)
		assert.NoError(t, err)
		assert.Len(t, items, 2)
		// First: root+label+2u+3k = 7. Second: root+label+2u (kstack 0 is
		// empty) = 4.
		assert.Len(t, items[0].Stack, 7)
		assert.Len(t, items[1].Stack, 4)
	})
}

func requireLen(t *testing.T, items []*profiler.TreeItem, want int) {
	t.Helper()
	if len(items) != want {
		t.Fatalf("got %d items, want %d", len(items), want)
	}
}

// mapMockBPF serves ReadMap/MapIDByName for simple key -> value tests.
var errMissingKey = errors.New("mock: key does not exist")

// orderMockBPF records the call order of the narrow snapshot interface so
// tests can lock the detach-before-read contract of readPostWindowSnapshot.
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

func (m *orderMockBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	m.calls = append(m.calls, "dump:"+name)
	return nil, nil
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
	u64Bytes := func(v uint64) []byte {
		buf := &bytes.Buffer{}
		_ = binary.Write(buf, binary.LittleEndian, v)
		return buf.Bytes()
	}

	t.Run("sums both streams", func(t *testing.T) {
		m := &mapMockBPF{
			ids:  map[string]uint32{"dropped_samples": 7},
			vals: map[uint32]map[uint32][]byte{7: {0: u64Bytes(5), 1: u64Bytes(7)}},
		}
		got, err := readDroppedSamples(m)
		assert.NoError(t, err)
		assert.Equal(t, uint64(12), got)
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

// TestReadKsoftirqdWindow covers the detach-time snapshot semantics: the
// entry is only read after the probes are detached, and a zero timestamp
// means ksoftirqd never serviced a softirq on the target cpu.
func TestReadKsoftirqdWindow(t *testing.T) {
	winBytes := func(ts uint64, vec uint32) []byte {
		var win irqRqVictimWindow
		win.Ts = ts
		win.Vec = vec
		buf := &bytes.Buffer{}
		_ = binary.Write(buf, binary.LittleEndian, &win)
		return buf.Bytes()
	}

	t.Run("ksoftirqd did not run", func(t *testing.T) {
		m := &mapMockBPF{
			ids:  map[string]uint32{"ksoftirqd_window": 7},
			vals: map[uint32]map[uint32][]byte{7: {0: winBytes(0, 0)}},
		}
		hit, vec, err := readKsoftirqdWindow(m)
		assert.NoError(t, err)
		assert.False(t, hit)
		assert.Zero(t, vec)
	})

	t.Run("ksoftirqd ran, last vector is preserved", func(t *testing.T) {
		m := &mapMockBPF{
			ids:  map[string]uint32{"ksoftirqd_window": 7},
			vals: map[uint32]map[uint32][]byte{7: {0: winBytes(123, 3)}},
		}
		hit, vec, err := readKsoftirqdWindow(m)
		assert.NoError(t, err)
		assert.True(t, hit)
		assert.Equal(t, uint32(3), vec)
	})

	t.Run("read failure is an error", func(t *testing.T) {
		tests := []struct {
			name string
			m    *mapMockBPF
		}{
			{
				name: "map missing from loaded object",
				m:    &mapMockBPF{ids: map[string]uint32{}},
			},
			{
				name: "backend error",
				m: &mapMockBPF{
					ids:  map[string]uint32{"ksoftirqd_window": 7},
					errs: map[uint32]error{7: errors.New("huatuo: backend error")},
				},
			},
			{
				name: "short value",
				m: &mapMockBPF{
					ids:  map[string]uint32{"ksoftirqd_window": 7},
					vals: map[uint32]map[uint32][]byte{7: {0: {0xff}}},
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, _, err := readKsoftirqdWindow(tt.m)
				assert.Error(t, err)
			})
		}
	})
}

// TestReadPostWindowSnapshotDetachBeforeReads locks the cut-off contract:
// the snapshot helper must detach the probes before any map read, so moving
// the Detach after a read would fail this test.
func TestReadPostWindowSnapshotDetachBeforeReads(t *testing.T) {
	winBytes := func(ts uint64, vec uint32) []byte {
		var win irqRqVictimWindow
		win.Ts = ts
		win.Vec = vec
		buf := &bytes.Buffer{}
		_ = binary.Write(buf, binary.LittleEndian, &win)
		return buf.Bytes()
	}
	u64Bytes := func(v uint64) []byte {
		buf := &bytes.Buffer{}
		_ = binary.Write(buf, binary.LittleEndian, v)
		return buf.Bytes()
	}

	m := &orderMockBPF{
		ids: map[string]uint32{
			"ksoftirqd_window": 1,
			"dropped_samples":  2,
			"rq_tasks":         3,
		},
		vals: map[uint32][]byte{
			1: winBytes(123, 3),
			// dropped_samples is read twice (source + victim keys); 6+6 = 12.
			2: u64Bytes(6),
		},
	}

	rqTasks, hit, vec, nmissed, err := readPostWindowSnapshot(m)
	assert.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, uint32(3), vec)
	assert.Equal(t, uint64(12), nmissed)
	assert.Empty(t, rqTasks)

	require.NotEmpty(t, m.calls)
	assert.Equal(t, "detach", m.calls[0], "Detach must be the first call: %v", m.calls)
	for i, call := range m.calls[1:] {
		assert.True(t,
			strings.HasPrefix(call, "mapid:") || call == "read" || strings.HasPrefix(call, "dump:"),
			"call %d %q: every post-detach call must be a map read, got %v", i, call, m.calls)
	}
}

// TestReadPostWindowSnapshotDetachFailureStopsReads locks the failure
// contract: when the detach fails, no map may be read and the error must
// propagate.
func TestReadPostWindowSnapshotDetachFailureStopsReads(t *testing.T) {
	m := &orderMockBPF{
		detachErr: errors.New("detach boom"),
		ids:       map[string]uint32{"ksoftirqd_window": 1},
	}

	_, _, _, _, err := readPostWindowSnapshot(m)
	assert.Error(t, err)
	assert.Equal(t, []string{"detach"}, m.calls)
}

func TestVecName(t *testing.T) {
	tests := []struct {
		vec  uint32
		want string
	}{
		{0, "HI"},
		{1, "TIMER"},
		{2, "NET_TX"},
		{3, "NET_RX"},
		{4, "BLOCK"},
		{5, "IRQ_POLL"},
		{6, "TASKLET"},
		{7, "SCHED"},
		{8, "HRTIMER"},
		{9, "RCU"},
		{10, "VEC10"},
		{99, "VEC99"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, vecName(tt.vec), "vecName(%d)", tt.vec)
	}
}

// ---------------------------------------------------------------------------
// buildFlameGraph
// ---------------------------------------------------------------------------

func TestBuildFlameGraph(t *testing.T) {
	t.Run("empty maps return nil", func(t *testing.T) {
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{},
			stacks:     map[uint32][]byte{},
			stackMapID: 99,
		}
		data, err := buildFlameGraph(b, nil, 3)
		assert.NoError(t, err)
		assert.Nil(t, data)
	})

	t.Run("merges source and victim entries into one profile", func(t *testing.T) {
		srcKey := &irqStackKey{Pid: 11, Vec: 3, UstackID: 1, KstackID: 2}
		victimKey := &irqStackKey{Pid: 22, Vec: 3, UstackID: 0, KstackID: 3}
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

		data, err := buildFlameGraph(b, nil, 3)
		assert.NoError(t, err)
		assert.NotNil(t, data)
		assert.Equal(t, profiler.ProfileTypeIrqTracingSample, data.ProfileType)
	})

	t.Run("merges rq victims with unknown stacks", func(t *testing.T) {
		b := &mockBPF{
			items:      map[string][]bpf.MapItem{},
			stacks:     map[uint32][]byte{},
			stackMapID: 99,
		}
		victims := []rqVictim{{pid: 123, comm: "app"}, {pid: 456, comm: "worker"}}
		data, err := buildFlameGraph(b, victims, 3)
		assert.NoError(t, err)
		assert.NotNil(t, data)
	})
}

// ---------------------------------------------------------------------------
// dumpRqTasks
// ---------------------------------------------------------------------------

func TestDumpRqTasks(t *testing.T) {
	rqItem := func(pid uint32, comm string) bpf.MapItem {
		key := &bytes.Buffer{}
		_ = binary.Write(key, binary.LittleEndian, pid)
		var task irqRqTask
		copy(task.Comm[:], comm)
		val := &bytes.Buffer{}
		_ = binary.Write(val, binary.LittleEndian, &task)
		return bpf.MapItem{Key: key.Bytes(), Value: val.Bytes()}
	}

	t.Run("returns tasks, skipping pid 0", func(t *testing.T) {
		items := []bpf.MapItem{
			rqItem(0, "swapper/3"), // idle task, skipped
			rqItem(123, "app"),     // valid
			rqItem(456, "worker"),  // valid
		}
		b := &mockBPF{items: map[string][]bpf.MapItem{"rq_tasks": items}}
		tasks, err := dumpRqTasks(b)
		assert.NoError(t, err)
		assert.Len(t, tasks, 2)
		assert.Equal(t, uint32(123), tasks[0].pid)
		assert.Equal(t, "app", tasks[0].comm)
		assert.Equal(t, uint32(456), tasks[1].pid)
		assert.Equal(t, "worker", tasks[1].comm)
	})

	t.Run("malformed entry returns error", func(t *testing.T) {
		items := []bpf.MapItem{
			{Key: []byte{0xff}, Value: nil}, // bad key
		}
		b := &mockBPF{items: map[string][]bpf.MapItem{"rq_tasks": items}}
		tasks, err := dumpRqTasks(b)
		assert.Error(t, err)
		assert.Empty(t, tasks)
	})

	t.Run("nil items return empty", func(t *testing.T) {
		b := &mockBPF{items: map[string][]bpf.MapItem{"rq_tasks": nil}}
		tasks, err := dumpRqTasks(b)
		assert.NoError(t, err)
		assert.Empty(t, tasks)
	})
}

// ---------------------------------------------------------------------------
// writeResult
// ---------------------------------------------------------------------------

func TestWriteResult(t *testing.T) {
	t.Run("writes json file and prints path", func(t *testing.T) {
		dir := t.TempDir()
		result := &IrqTracingResult{
			RqTasks: []string{"app(123)"},
			FlameData: &profiler.ProfileData{
				ProfileType: profiler.ProfileTypeIrqTracingSample,
			},
		}
		err := writeResult(dir, result)
		assert.NoError(t, err)

		entries, err := filepath.Glob(filepath.Join(dir, "irqtracing_*.json"))
		assert.NoError(t, err)
		assert.Len(t, entries, 1)

		data, err := os.ReadFile(entries[0])
		assert.NoError(t, err)
		assert.Contains(t, string(data), profiler.ProfileTypeIrqTracingSample)
		assert.Contains(t, string(data), "app(123)")

		var parsed IrqTracingResult
		assert.NoError(t, json.Unmarshal(data, &parsed))
		assert.Equal(t, []string{"app(123)"}, parsed.RqTasks)
	})

	t.Run("fails on invalid output path", func(t *testing.T) {
		err := writeResult("/nonexistent-dir-xyz/", &IrqTracingResult{})
		assert.Error(t, err)
	})
}
