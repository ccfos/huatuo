// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Throttle tests keep statistics, hook compatibility, and BPF lifecycle contracts together.
package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Statistics, CSS attribution, and interval baselines.

// Per-CPU snapshot and metric fixtures.

const throtlBLKGOwnerMap = "throtl_blkg_owner_map"

type throtlTestDump struct {
	items []bpf.MapItem
	err   error
}

type fakeThrotlCollectorBPF struct {
	bpf.BPF
	dumps         []throtlTestDump
	mapDumps      map[string][]throtlTestDump
	lastAggregate []bpf.MapItem
	statusMapID   uint32
	statusValue   []byte
	statusErr     error
	dumpCalls     int
	mapDumpCalls  map[string]int
	statusCalls   int
	dumpHook      func()
	events        []string
}

func newFakeThrotlCollectorBPF() *fakeThrotlCollectorBPF {
	return &fakeThrotlCollectorBPF{
		statusMapID:  1,
		statusValue:  make([]byte, binary.Size(throtlStatus{})),
		mapDumps:     make(map[string][]throtlTestDump),
		mapDumpCalls: make(map[string]int),
	}
}

func (b *fakeThrotlCollectorBPF) DumpMapByName(
	name string,
) ([]bpf.MapItem, error) {
	b.mapDumpCalls[name]++
	if dumps, scripted := b.mapDumps[name]; scripted {
		if len(dumps) == 0 {
			return nil, fmt.Errorf("unexpected extra %s dump", name)
		}
		result := dumps[0]
		b.mapDumps[name] = dumps[1:]
		return result.items, result.err
	}
	switch name {
	case throtlTDMap:
		return throtlTestTDs(b.lastAggregate), nil
	case throtlPendingMap:
		return nil, nil
	case throtlWaitAggregateMap:
	default:
		return nil, fmt.Errorf("unexpected map %q", name)
	}
	b.dumpCalls++
	b.events = append(b.events, "dump")
	if len(b.dumps) == 0 {
		return nil, fmt.Errorf("unexpected extra %s dump", name)
	}
	result := b.dumps[0]
	b.dumps = b.dumps[1:]
	if b.dumpHook != nil {
		b.dumpHook()
	}
	b.lastAggregate = append([]bpf.MapItem(nil), result.items...)
	return result.items, result.err
}

func (b *fakeThrotlCollectorBPF) DumpMapBatch(
	mapID uint32,
) ([]bpf.MapItem, error) {
	if mapID != b.MapIDByName(throtlWaitAggregateMap) {
		return nil, fmt.Errorf("unexpected batch map ID %d", mapID)
	}
	return b.DumpMapByName(throtlWaitAggregateMap)
}

func (b *fakeThrotlCollectorBPF) MapIDByName(name string) uint32 {
	switch name {
	case throtlStatusMap:
		return b.statusMapID
	case throtlWaitAggregateMap:
		return 2
	case throtlPendingMap:
		return 4
	case throtlTDMap:
		return 5
	default:
		return 0
	}
}

func (b *fakeThrotlCollectorBPF) ReadMap(
	mapID uint32,
	key []byte,
) ([]byte, error) {
	b.statusCalls++
	b.events = append(b.events, "status")
	if mapID != b.statusMapID {
		return nil, fmt.Errorf("unexpected status map ID %d", mapID)
	}
	if len(key) != 4 || binary.LittleEndian.Uint32(key) != 0 {
		return nil, fmt.Errorf("unexpected status key %v", key)
	}
	return b.statusValue, b.statusErr
}

func (b *fakeThrotlCollectorBPF) setDumps(results ...throtlTestDump) {
	b.dumps = append([]throtlTestDump(nil), results...)
}

func (b *fakeThrotlCollectorBPF) setMapDumps(
	name string,
	results ...throtlTestDump,
) {
	b.mapDumps[name] = append([]throtlTestDump(nil), results...)
}

type reusedThrotlCollectorBPF struct {
	*fakeThrotlCollectorBPF
	batchDumps []throtlTestDump
}

func (b *reusedThrotlCollectorBPF) DumpMapBatch(
	mapID uint32,
) ([]bpf.MapItem, error) {
	if mapID != b.MapIDByName(throtlWaitAggregateMap) {
		return nil, fmt.Errorf("unexpected batch map ID %d", mapID)
	}
	if len(b.batchDumps) == 0 {
		return nil, fmt.Errorf("unexpected extra %s batch dump", throtlWaitAggregateMap)
	}
	result := b.batchDumps[0]
	b.batchDumps = b.batchDumps[1:]
	b.lastAggregate = append([]bpf.MapItem(nil), result.items...)
	return result.items, result.err
}

type throtlTestKey struct {
	td        uint64
	blkg      uint64
	serial    uint64
	css       uint64
	major     uint32
	minor     uint32
	operation uint32
}

type throtlTestLane struct {
	count    uint64
	wait10US uint64
}

type throtlTestBLKGOwner struct {
	TD        uint64
	CSS       uint64
	CSSSerial uint64
}

func stableThrotlTestLane(count, wait10US uint64) throtlTestLane {
	return throtlTestLane{
		count:    count,
		wait10US: wait10US,
	}
}

func throtlTestKeyBytes(key throtlTestKey) []byte {
	key = normalizedThrotlTestKey(key)
	data := make([]byte, 40)
	binary.LittleEndian.PutUint64(data[0:8], key.td)
	binary.LittleEndian.PutUint64(data[8:16], key.blkg)
	binary.LittleEndian.PutUint64(data[16:24], key.css)
	binary.LittleEndian.PutUint64(data[24:32], key.serial)
	binary.LittleEndian.PutUint32(data[32:36], key.operation)
	return data
}

func normalizedThrotlTestKey(key throtlTestKey) throtlTestKey {
	if key.td == 0 {
		key.td = uint64(key.major)<<32 | uint64(key.minor)
	}
	if key.serial == 0 {
		key.serial = key.css
	}
	if key.css == 0 {
		key.css = key.serial
	}
	if key.blkg == 0 {
		key.blkg = key.td ^ key.serial*0x9e3779b97f4a7c15
		if key.blkg == 0 {
			key.blkg = 1
		}
	}
	return key
}

func throtlTestRawKey(key throtlTestKey) throtlWaitKey {
	key = normalizedThrotlTestKey(key)
	return throtlWaitKey{
		TD: key.td, BLKG: key.blkg, CSS: key.css, CSSSerial: key.serial,
		Operation: key.operation,
	}
}

func decodeThrotlTestKey(data []byte) (throtlWaitKey, bool) {
	if len(data) != 40 {
		return throtlWaitKey{}, false
	}
	return throtlWaitKey{
		TD:        binary.LittleEndian.Uint64(data[0:8]),
		BLKG:      binary.LittleEndian.Uint64(data[8:16]),
		CSS:       binary.LittleEndian.Uint64(data[16:24]),
		CSSSerial: binary.LittleEndian.Uint64(data[24:32]),
		Operation: binary.LittleEndian.Uint32(data[32:36]),
	}, true
}

func throtlTestTDs(aggregate []bpf.MapItem) []bpf.MapItem {
	tds := make(map[uint64]throtlTDValue)
	for _, item := range aggregate {
		key, ok := decodeThrotlTestKey(item.Key)
		if !ok {
			continue
		}
		tds[key.TD] = throtlTDValue{
			State: throtlTDActive,
			Major: uint32(key.TD >> 32),
			Minor: uint32(key.TD),
		}
	}
	items := make([]bpf.MapItem, 0, len(tds))
	for td, value := range tds {
		items = append(items, bpf.MapItem{
			Key:   encodeThrotlTestDataValue(td),
			Value: encodeThrotlTestDataValue(value),
		})
	}
	return items
}

func encodeThrotlTestDataValue(value any) []byte {
	buffer := bytes.NewBuffer(make([]byte, 0, binary.Size(value)))
	if err := binary.Write(buffer, binary.LittleEndian, value); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

func throtlTestValueBytes(lanes ...throtlTestLane) []byte {
	data := make([]byte, len(lanes)*throtlWaitValueSize)
	for index, lane := range lanes {
		offset := index * throtlWaitValueSize
		packed := (lane.count << throtlWait10USBits) | lane.wait10US
		binary.LittleEndian.PutUint64(
			data[offset:offset+throtlWaitValueSize], packed)
	}
	return data
}

func throtlTestItem(
	key throtlTestKey,
	lanes ...throtlTestLane,
) bpf.MapItem {
	return bpf.MapItem{
		Key:   throtlTestKeyBytes(key),
		Value: throtlTestValueBytes(lanes...),
	}
}

func newThrotlTestCollector(
	t *testing.T,
	object bpf.BPF,
	possibleCPUs int,
) *throtlTracing {
	t.Helper()
	breaker, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() { cancel(nil) })
	return &throtlTracing{session: &throtlSession{
		object:          object,
		possibleCPUs:    possibleCPUs,
		previous:        make(throtlWaitSnapshot),
		breaker:         breaker,
		cancel:          cancel,
		containerSource: emptyThrotlTestContainerSource,
	}}
}

func emptyThrotlTestContainerSource() (map[string]*pod.Container, error) {
	return nil, nil
}

type inspectedThrotlMetric struct {
	name      string
	help      string
	valueType int
	value     float64
	labels    map[string]string
}

func inspectThrotlMetric(
	t *testing.T,
	data *metric.Data,
) inspectedThrotlMetric {
	t.Helper()
	require.NotNil(t, data)

	return inspectedThrotlMetric{
		name:      data.Name(),
		help:      data.Help(),
		valueType: data.Type(),
		value:     data.Value,
		labels:    data.Labels(),
	}
}

func throtlMetricByNameAndScope(
	t *testing.T,
	metrics []*metric.Data,
	wantName string,
	wantScope string,
) inspectedThrotlMetric {
	t.Helper()
	var matches []inspectedThrotlMetric
	for _, data := range metrics {
		got := inspectThrotlMetric(t, data)
		if got.name == wantName && got.labels["scope"] == wantScope {
			matches = append(matches, got)
		}
	}
	require.Len(t, matches, 1, "metric %s scope %s", wantName, wantScope)
	return matches[0]
}

func throtlCustomLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		if key == metric.LabelHost || key == metric.LabelRegion {
			continue
		}
		result[key] = value
	}
	return result
}

func requireThrotlHostAndOtherMetrics(
	t *testing.T,
	metrics []*metric.Data,
	device string,
	operation string,
	wantCount float64,
	wantAverageMS float64,
) {
	t.Helper()
	require.Len(t, metrics, 4)
	for _, scope := range []string{throtlHostScope, throtlOtherScope} {
		wantLabels := map[string]string{
			"device":    device,
			"operation": operation,
			"scope":     scope,
		}

		count := throtlMetricByNameAndScope(t, metrics,
			"delayed_io_count", scope)
		require.Equal(t, metric.MetricTypeGauge, count.valueType)
		require.Equal(t, wantCount, count.value)
		require.Equal(t, wantLabels, throtlCustomLabels(count.labels))
		require.NotEmpty(t, count.help)
		require.Contains(t, strings.ToLower(count.help), "throttle")

		average := throtlMetricByNameAndScope(t, metrics,
			"average_wait_milliseconds", scope)
		require.Equal(t, metric.MetricTypeGauge, average.valueType)
		require.Equal(t, wantAverageMS, average.value)
		require.Equal(t, wantLabels, throtlCustomLabels(average.labels))
		require.NotEmpty(t, average.help)
		require.Contains(t, strings.ToLower(average.help), "throttle")
	}
}

// Strict map decoding and raw collection boundaries.

func TestThrotlBlockWaitStrictSnapshotDecoding(t *testing.T) {
	require.Equal(t, "7:0", ioControlDeviceName(7, 0),
		"blk-throttle must keep the public major:minor identity")

	key := throtlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	valid := throtlTestItem(key, stableThrotlTestLane(10, 1_000))

	shortKey := valid
	shortKey.Key = shortKey.Key[:len(shortKey.Key)-1]
	longKey := valid
	longKey.Key = append(append([]byte(nil), longKey.Key...), 0)
	shortValue := valid
	shortValue.Value = shortValue.Value[:len(shortValue.Value)-1]
	longValue := valid
	longValue.Value = append(append([]byte(nil), longValue.Value...), 0)

	for _, test := range []struct {
		name         string
		possibleCPUs int
		items        []bpf.MapItem
	}{
		{name: "short key", possibleCPUs: 1, items: []bpf.MapItem{shortKey}},
		{name: "long key", possibleCPUs: 1, items: []bpf.MapItem{longKey}},
		{name: "short value", possibleCPUs: 1, items: []bpf.MapItem{shortValue}},
		{name: "long value", possibleCPUs: 1, items: []bpf.MapItem{longValue}},
		{
			name: "possible CPU value length", possibleCPUs: 2,
			items: []bpf.MapItem{valid},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeThrotlCollectorBPF()
			object.setDumps(throtlTestDump{items: test.items})
			collector := newThrotlTestCollector(t, object, test.possibleCPUs)

			_, err := collector.Update()
			require.Error(t, err)
			require.Equal(t, 1, object.dumpCalls,
				"decode errors must not retry the dump")
			require.Zero(t, object.statusCalls,
				"health is checked only after a complete raw decode")
		})
	}
}

func TestThrotlSnapshotResolvesOnlyActiveTDs(t *testing.T) {
	keys := []throtlWaitKey{
		// Separate blkg records under one td and CSS remain distinct.
		{TD: 1, BLKG: 0xb1, CSS: 0xa1, CSSSerial: 101, Operation: 0},
		{TD: 1, BLKG: 0xb2, CSS: 0xa1, CSSSerial: 101, Operation: 0},
		{TD: 2, BLKG: 0xb3, CSS: 0xa2, CSSSerial: 202, Operation: 0},
		{TD: 3, BLKG: 0xb4, CSS: 0xa3, CSSSerial: 303, Operation: 0},
	}
	raw := make(throtlWaitSnapshot)
	for _, key := range keys {
		raw[key] = throtlWaitSample{
			operation: "read",
			counters:  []throtlWaitCounters{{DelayedCount: 1, Wait10US: 1}},
		}
	}
	tds := throtlTDSnapshot{
		1: {State: throtlTDActive, Major: 8, Minor: 1},
		3: {State: throtlTDRetired, Major: 8, Minor: 3},
	}

	// Resolve the captured map in place before it becomes the next baseline.
	resolveThrotlWaitDevices(raw, tds)
	require.Equal(t, throtlWaitSnapshot{
		keys[0]: {
			device:    "8:1",
			operation: "read",
			counters:  []throtlWaitCounters{{DelayedCount: 1, Wait10US: 1}},
		},
		keys[1]: {
			device:    "8:1",
			operation: "read",
			counters:  []throtlWaitCounters{{DelayedCount: 1, Wait10US: 1}},
		},
	}, raw)
}

func TestThrotlWaitCounterDecoding(t *testing.T) {
	t.Run("26-bit count and 38-bit wait lanes", func(t *testing.T) {
		data := make([]byte, 2*throtlWaitValueSize)
		binary.LittleEndian.PutUint64(data[0:8], uint64(3)<<38|11)
		binary.LittleEndian.PutUint64(data[8:16], uint64(5)<<38|13)
		got, err := decodeThrotlWaitCounters(data, 2)
		require.NoError(t, err)
		require.Equal(t, []throtlWaitCounters{
			{DelayedCount: 3, Wait10US: 11},
			{DelayedCount: 5, Wait10US: 13},
		}, got)
	})

	t.Run("field limits", func(t *testing.T) {
		data := throtlTestValueBytes(
			throtlTestLane{
				count:    throtlDelayedCountMask,
				wait10US: throtlWait10USMask,
			},
		)
		got, err := decodeThrotlWaitCounters(data, 1)
		require.NoError(t, err)
		require.Equal(t, []throtlWaitCounters{{
			DelayedCount: throtlDelayedCountMask,
			Wait10US:     throtlWait10USMask,
		}}, got)
	})

	t.Run("one aggregate capture without owner dump", func(t *testing.T) {
		key := throtlTestKey{
			css: 1, major: 4095, minor: 1, operation: 0,
		}
		object := newFakeThrotlCollectorBPF()
		object.setDumps(
			throtlTestDump{items: []bpf.MapItem{
				throtlTestItem(key, stableThrotlTestLane(3, 3)),
			}},
			throtlTestDump{err: errors.New("unexpected second capture")},
		)

		metrics, err := newThrotlTestCollector(t, object, 1).Update()
		require.NoError(t, err)
		require.Equal(t, 1, object.dumpCalls)
		require.Zero(t, object.mapDumpCalls[throtlBLKGOwnerMap])
		requireThrotlHostAndOtherMetrics(
			t, metrics, "4095:1", "read", 3, 0.01)
	})

	t.Run("exact value size", func(t *testing.T) {
		for _, size := range []int{
			0, throtlWaitValueSize - 1,
			throtlWaitValueSize + 1,
		} {
			_, err := decodeThrotlWaitCounters(make([]byte, size), 1)
			require.Error(t, err, "value size %d", size)
		}
	})

	t.Run("valid CPU count", func(t *testing.T) {
		maxInt := int(^uint(0) >> 1)
		for _, possibleCPUs := range []int{
			0,
			-1,
			maxInt/throtlWaitValueSize + 1,
		} {
			_, err := decodeThrotlWaitCounters(nil, possibleCPUs)
			require.Error(t, err, "possible CPUs %d", possibleCPUs)
		}
	})
}

func TestThrotlBlockWaitRejectsUnknownOperation(t *testing.T) {
	object := newFakeThrotlCollectorBPF()
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(throtlTestKey{
			css: 1, major: 4095, minor: 1, operation: 2,
		}, stableThrotlTestLane(1, 100)),
	}})
	collector := newThrotlTestCollector(t, object, 1)

	_, err := collector.Update()
	require.Error(t, err)
	require.Equal(t, 1, object.dumpCalls)
	require.Zero(t, object.statusCalls)
}

func TestThrotlSnapshotRetriesWholeBoundary(t *testing.T) {
	key := throtlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	baseline := []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(10, 1_000)),
	}
	poison := []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(1_000, 100_000)),
	}
	current := []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(15, 2_000)),
	}
	duplicateAggregate := append([]bpf.MapItem(nil), poison...)
	duplicateAggregate = append(duplicateAggregate,
		throtlTestItem(key,
			stableThrotlTestLane(1_001, 100_100)))
	duplicateTDs := throtlTestTDs(poison)
	duplicateTDs = append(duplicateTDs, duplicateTDs[0])

	for _, test := range []struct {
		name      string
		configure func(*fakeThrotlCollectorBPF)
		wantCalls map[string]int
	}{
		{
			name: "aggregate duplicate cursor",
			configure: func(object *fakeThrotlCollectorBPF) {
				object.setDumps(
					throtlTestDump{items: duplicateAggregate},
					throtlTestDump{items: current},
				)
			},
			wantCalls: map[string]int{
				throtlWaitAggregateMap: 2,
				throtlTDMap:            1,
			},
		},
		{
			name: "td duplicate cursor",
			configure: func(object *fakeThrotlCollectorBPF) {
				object.setDumps(
					throtlTestDump{items: poison},
					throtlTestDump{items: current},
				)
				object.setMapDumps(throtlTDMap,
					throtlTestDump{items: duplicateTDs},
					throtlTestDump{items: throtlTestTDs(current)},
				)
			},
			wantCalls: map[string]int{
				throtlWaitAggregateMap: 2,
				throtlTDMap:            2,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeThrotlCollectorBPF()
			collector := newThrotlTestCollector(t, object, 1)
			object.setDumps(throtlTestDump{items: baseline})
			_, err := collector.Update()
			require.NoError(t, err)

			before := make(map[string]int, len(object.mapDumpCalls))
			for name, calls := range object.mapDumpCalls {
				before[name] = calls
			}
			test.configure(object)
			metrics, err := collector.Update()
			require.NoError(t, err)
			gotCalls := make(map[string]int, len(test.wantCalls))
			for name := range test.wantCalls {
				gotCalls[name] = object.mapDumpCalls[name] - before[name]
			}
			require.Equal(t, test.wantCalls, gotCalls)
			require.Nil(t, context.Cause(collector.session.breaker))
			requireThrotlHostAndOtherMetrics(t, metrics,
				"4095:1", "read", 5, 2)
		})
	}
}

// Interval baselines, retries, health stops, and per-CPU wrap.

func TestThrotlIntervalStartsFromEmptyMapAndRestarts(t *testing.T) {
	var unstarted throtlTracing
	metrics, err := unstarted.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Nil(t, metrics)

	key := throtlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	object := newFakeThrotlCollectorBPF()
	collector := newThrotlTestCollector(t, object, 1)

	// Every BPF object owns a freshly-created empty aggregate map. Completed
	// episodes before its first scrape therefore have a known zero predecessor.
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(3, 900)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "read", 3, 3)

	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(5, 1_500)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "read", 2, 3)

	// A replacement object starts another known-zero counter generation.
	replacementObject := newFakeThrotlCollectorBPF()
	replacement := newThrotlTestCollector(t, replacementObject, 1).session
	collector.session = replacement
	replacementObject.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(100, 30_000)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "read", 100, 3)

	replacementObject.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(102, 30_600)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "read", 2, 3)

	collector.session = nil
	metrics, err = collector.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Empty(t, metrics)
}

func TestThrotlRetryableIntervalFailuresRebuildBaseline(t *testing.T) {
	key := throtlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	baseline := throtlTestItem(key,
		stableThrotlTestLane(10, 1_000))
	current := throtlTestItem(key,
		stableThrotlTestLane(15, 2_000))
	next := throtlTestItem(key,
		stableThrotlTestLane(17, 2_800))

	for _, test := range []struct {
		name       string
		initial    bool
		configure  func(*fakeThrotlCollectorBPF)
		failure    throtlTestDump
		wantEvents []string
	}{
		{
			name:       "initial aggregate dump",
			initial:    true,
			failure:    throtlTestDump{err: errors.New("aggregate dump failed")},
			wantEvents: []string{"dump"},
		},
		{
			name:       "aggregate dump",
			failure:    throtlTestDump{err: errors.New("aggregate dump failed")},
			wantEvents: []string{"dump"},
		},
		{
			name: "td dump",
			configure: func(object *fakeThrotlCollectorBPF) {
				object.setMapDumps(throtlTDMap,
					throtlTestDump{err: errors.New("td dump failed")})
			},
			failure:    throtlTestDump{items: []bpf.MapItem{current}},
			wantEvents: []string{"dump"},
		},
		{
			name: "aggregate decode",
			failure: throtlTestDump{items: []bpf.MapItem{{
				Key: throtlTestKeyBytes(key), Value: []byte{0},
			}}},
			wantEvents: []string{"dump"},
		},
		{
			name: "status read",
			configure: func(object *fakeThrotlCollectorBPF) {
				object.statusErr = errors.New("status read failed")
			},
			failure:    throtlTestDump{items: []bpf.MapItem{current}},
			wantEvents: []string{"dump", "status"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeThrotlCollectorBPF()
			collector := newThrotlTestCollector(t, object, 1)
			if !test.initial {
				object.setDumps(throtlTestDump{items: []bpf.MapItem{baseline}})
				_, err := collector.Update()
				require.NoError(t, err)
			}

			for range 2 {
				object.events = nil
				if test.configure != nil {
					test.configure(object)
				}
				object.setDumps(test.failure)
				metrics, err := collector.Update()
				require.Error(t, err)
				require.Empty(t, metrics)
				require.Equal(t, test.wantEvents, object.events)
				require.Nil(t, context.Cause(collector.session.breaker))
			}

			delete(object.mapDumps, throtlTDMap)
			object.statusMapID = 1
			object.statusErr = nil
			object.statusValue = make([]byte, binary.Size(throtlStatus{}))
			object.setDumps(throtlTestDump{items: []bpf.MapItem{current}})
			metrics, err := collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Empty(t, metrics)
			require.Nil(t, context.Cause(collector.session.breaker))

			object.setDumps(throtlTestDump{items: []bpf.MapItem{next}})
			metrics, err = collector.Update()
			require.NoError(t, err)
			requireThrotlHostAndOtherMetrics(t, metrics,
				"4095:1", "read", 2, 4)
		})
	}
}

func TestThrotlBatchErrorsDoNotFallbackOrAdvanceBaseline(t *testing.T) {
	key := throtlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	baseline := throtlTestItem(key, stableThrotlTestLane(10, 1_000))
	current := throtlTestItem(key, stableThrotlTestLane(15, 2_000))
	next := throtlTestItem(key, stableThrotlTestLane(17, 2_800))
	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "EIO", err: unix.EIO},
		{name: "EPERM", err: unix.EPERM},
		{name: "EBADF", err: unix.EBADF},
		{name: "ENOMEM", err: unix.ENOMEM},
	} {
		for _, partial := range []struct {
			name  string
			items []bpf.MapItem
		}{
			{name: "before any rows"},
			{
				name: "after partial rows",
				items: []bpf.MapItem{
					throtlTestItem(key, stableThrotlTestLane(0, 0)),
				},
			},
		} {
			t.Run(failure.name+"/"+partial.name, func(t *testing.T) {
				object := &reusedThrotlCollectorBPF{
					fakeThrotlCollectorBPF: newFakeThrotlCollectorBPF(),
					batchDumps: []throtlTestDump{
						{items: []bpf.MapItem{baseline}},
						{items: partial.items, err: failure.err},
						{items: []bpf.MapItem{current}},
						{items: []bpf.MapItem{next}},
					},
				}
				collector := newThrotlTestCollector(t, object, 1)
				_, err := collector.Update()
				require.NoError(t, err)

				// An available single-key dump must not hide an unrelated
				// batch failure or commit the partial rows as a baseline.
				object.setDumps(throtlTestDump{items: []bpf.MapItem{current}})
				metrics, err := collector.Update()
				require.ErrorIs(t, err, failure.err)
				require.Empty(t, metrics)
				require.Zero(t, object.dumpCalls)
				require.Nil(t, context.Cause(collector.session.breaker))
				require.Equal(t, []throtlWaitCounters{{
					DelayedCount: 10, Wait10US: 1_000,
				}}, collector.session.previous[throtlTestRawKey(key)].counters)

				metrics, err = collector.Update()
				require.ErrorIs(t, err, metric.ErrNoData)
				require.Empty(t, metrics)
				require.Zero(t, object.dumpCalls)

				metrics, err = collector.Update()
				require.NoError(t, err)
				requireThrotlHostAndOtherMetrics(t, metrics,
					"4095:1", "read", 2, 4)
				require.Zero(t, object.dumpCalls)
			})
		}
	}
}

func TestThrotlSingleLookupRequiresLegacyHashAllocator(t *testing.T) {
	word := &btf.Int{Name: "unsigned long", Size: 8, Encoding: btf.Unsigned}
	allocator := &btf.Struct{
		Name: "bpf_mem_alloc", Size: 8,
		Members: []btf.Member{{Name: "cache", Type: word}},
	}
	socket := &btf.Struct{
		Name: "bpf_htab", Size: 8,
		Members: []btf.Member{{Name: "progs", Type: word}},
	}
	legacyHash := ioControlHashBTFFixture()
	allocatedHash := ioControlHashBTFFixture(btf.Member{Name: "ma", Type: allocator})
	for _, test := range []struct {
		name  string
		types []btf.Type
		want  string
		cause error
	}{
		{name: "legacy HASH", types: []btf.Type{legacyHash}},
		{
			name:  "unrelated allocator type",
			types: []btf.Type{legacyHash, allocator},
		},
		{
			name:  "socket HASH precedes ordinary HASH",
			types: []btf.Type{socket, legacyHash},
		},
		{
			name:  "ordinary HASH precedes socket HASH",
			types: []btf.Type{legacyHash, socket},
		},
		{
			name:  "HASH uses BPF allocator",
			types: []btf.Type{allocatedHash},
			want:  "bpf_mem_alloc",
		},
		{
			name: "per-CPU HASH allocator",
			types: []btf.Type{ioControlHashBTFFixture(
				btf.Member{Name: "pcpu_ma", Type: allocator})},
			want: "bpf_mem_alloc",
		},
		{
			name: "allocator behind type qualifiers",
			types: []btf.Type{ioControlHashBTFFixture(btf.Member{
				Name: "allocator", Type: &btf.Typedef{
					Name: "allocator_type", Type: &btf.Const{Type: allocator},
				},
			})},
			want: "bpf_mem_alloc",
		},
		{
			name:  "legacy candidate precedes unsafe HASH",
			types: []btf.Type{legacyHash, allocatedHash},
			want:  "bpf_mem_alloc",
		},
		{
			name:  "unsafe HASH precedes legacy candidate",
			types: []btf.Type{allocatedHash, legacyHash},
			want:  "bpf_mem_alloc",
		},
		{
			name:  "missing HASH type",
			types: []btf.Type{word}, cause: btf.ErrNotFound,
		},
		{
			name:  "only socket HASH",
			types: []btf.Type{socket}, cause: btf.ErrNotFound,
		},
		{
			name:  "incomplete HASH type",
			types: []btf.Type{&btf.Struct{Name: "bpf_htab"}}, cause: btf.ErrNotFound,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder, err := btf.NewBuilder(test.types)
			require.NoError(t, err)
			raw, err := builder.Marshal(nil, nil)
			require.NoError(t, err)
			spec, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
			require.NoError(t, err)
			err = checkThrotlSingleLookup(spec)
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
			} else if test.cause != nil {
				require.ErrorIs(t, err, test.cause)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestThrotlTerminalHealthFailuresStopSession(t *testing.T) {
	key := throtlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	current := throtlTestItem(key,
		stableThrotlTestLane(15, 2_000))

	for _, test := range []struct {
		name      string
		configure func(*fakeThrotlCollectorBPF)
	}{
		{
			name: "missing status map",
			configure: func(object *fakeThrotlCollectorBPF) {
				object.statusMapID = 0
			},
		},
		{
			name: "short status",
			configure: func(object *fakeThrotlCollectorBPF) {
				object.statusValue = make(
					[]byte, binary.Size(throtlStatus{})-1,
				)
			},
		},
		{
			name: "long status",
			configure: func(object *fakeThrotlCollectorBPF) {
				object.statusValue = make(
					[]byte, binary.Size(throtlStatus{})+1,
				)
			},
		},
		{
			name: "unhealthy status",
			configure: func(object *fakeThrotlCollectorBPF) {
				object.statusValue = encodeThrotlTestData(t,
					throtlStatus{
						Reason: throtlFailureLifecycle,
					})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeThrotlCollectorBPF()
			collector := newThrotlTestCollector(t, object, 1)
			test.configure(object)
			object.setDumps(throtlTestDump{items: []bpf.MapItem{current}})

			_, err := collector.Update()
			require.Error(t, err)
			require.ErrorIs(t, err, types.ErrTracingStopped)
			require.ErrorIs(t, context.Cause(collector.session.breaker), types.ErrTracingStopped)
		})
	}
}

func TestThrotlIntervalHandlesPerCPUWrap(t *testing.T) {
	for _, test := range []struct {
		name     string
		previous throtlWaitCounters
		current  throtlWaitCounters
		want     throtlWaitCounters
	}{
		{
			name: "count field",
			previous: throtlWaitCounters{
				DelayedCount: throtlDelayedCountMask - 1,
				Wait10US:     100,
			},
			current: throtlWaitCounters{DelayedCount: 1, Wait10US: 200},
			want:    throtlWaitCounters{DelayedCount: 3, Wait10US: 100},
		},
		{
			name: "wait field",
			previous: throtlWaitCounters{
				DelayedCount: 10,
				Wait10US:     throtlWait10USMask - 2,
			},
			current: throtlWaitCounters{DelayedCount: 12, Wait10US: 297},
			want:    throtlWaitCounters{DelayedCount: 2, Wait10US: 300},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := deltaThrotlWaitCounters(
				[]throtlWaitCounters{test.previous},
				[]throtlWaitCounters{test.current},
			)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}

	key := throtlTestKey{css: 1, major: 4095, minor: 1, operation: 0}

	object := newFakeThrotlCollectorBPF()
	collector := newThrotlTestCollector(t, object, 1)
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(
			throtlDelayedCountMask-1, throtlWait10USMask-2)),
	}})
	_, err := collector.Update()
	require.NoError(t, err)

	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(1, 297)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "read", 3, 1)

	wrapped, err := decodeThrotlWaitCounters(
		throtlTestValueBytes(stableThrotlTestLane(0, 1)),
		1,
	)
	require.NoError(t, err)
	require.Equal(t, []throtlWaitCounters{{Wait10US: 1}}, wrapped)
}

func TestThrotlIntervalRejectsWaitWithoutDelayedIO(t *testing.T) {
	key := throtlTestKey{css: 1, major: 4095, minor: 1, operation: 0}

	t.Run("delta", func(t *testing.T) {
		object := newFakeThrotlCollectorBPF()
		collector := newThrotlTestCollector(t, object, 1)
		object.setDumps(throtlTestDump{items: []bpf.MapItem{
			throtlTestItem(key,
				stableThrotlTestLane(10, 1_000)),
		}})
		_, err := collector.Update()
		require.NoError(t, err)

		object.setDumps(throtlTestDump{items: []bpf.MapItem{
			throtlTestItem(key,
				stableThrotlTestLane(10, 1_100)),
		}})
		_, err = collector.Update()
		require.Error(t, err)

		object.setDumps(throtlTestDump{items: []bpf.MapItem{
			throtlTestItem(key,
				stableThrotlTestLane(15, 2_000)),
		}})
		metrics, err := collector.Update()
		require.ErrorIs(t, err, metric.ErrNoData)
		require.Empty(t, metrics)

		object.setDumps(throtlTestDump{items: []bpf.MapItem{
			throtlTestItem(key,
				stableThrotlTestLane(17, 2_800)),
		}})
		metrics, err = collector.Update()
		require.NoError(t, err)
		requireThrotlHostAndOtherMetrics(t, metrics,
			"4095:1", "read", 2, 4)
	})
}

func TestThrotlSnapshotDoesNotReadReusedAggregateValue(t *testing.T) {
	key := throtlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	baseline := throtlTestItem(key, stableThrotlTestLane(100, 10_000))
	current := throtlTestItem(key, stableThrotlTestLane(101, 10_200))
	object := &reusedThrotlCollectorBPF{
		fakeThrotlCollectorBPF: newFakeThrotlCollectorBPF(),
		batchDumps: []throtlTestDump{
			{items: []bpf.MapItem{baseline}},
			{items: []bpf.MapItem{current}},
		},
	}
	// A single-key lookup can retain the old key while its map storage is
	// deleted and reused for a new counter. Batch copying keeps them paired.
	object.setDumps(
		throtlTestDump{items: []bpf.MapItem{baseline}},
		throtlTestDump{items: []bpf.MapItem{
			throtlTestItem(key, stableThrotlTestLane(0, 0)),
		}},
	)
	collector := newThrotlTestCollector(t, object, 1)
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "read", 100, 1)

	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "read", 1, 2)
}

// Host metrics preserve weighted aggregates and dump-order independence.

func TestThrotlAggregateHostWeightedAverage(t *testing.T) {
	keyA := throtlTestKey{css: 10, major: 4095, minor: 1, operation: 1}
	keyB := throtlTestKey{css: 20, major: 4095, minor: 1, operation: 1}
	object := newFakeThrotlCollectorBPF()
	collector := newThrotlTestCollector(t, object, 1)
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(keyA, stableThrotlTestLane(100, 10_000)),
		throtlTestItem(keyB, stableThrotlTestLane(200, 20_000)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "write", 300, 1)

	// A contributes one episode at 1ms and B contributes three at 3ms. The
	// host average is (1+9)/4 = 2.5ms, not the unweighted (1+3)/2 = 2ms.
	current := []bpf.MapItem{
		throtlTestItem(keyA, stableThrotlTestLane(101, 10_100)),
		throtlTestItem(keyB, stableThrotlTestLane(203, 20_900)),
	}
	object.setDumps(throtlTestDump{items: current})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "write", 4, 2.5)

	object.setDumps(throtlTestDump{items: current})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics,
		"4095:1", "write", 0, 0)
}

func TestThrotlAggregateMetricsIgnoreDumpOrder(t *testing.T) {
	keys := []throtlTestKey{
		{css: 3, major: 4095, minor: 1, operation: 1},
		{css: 2, major: 4094, minor: 2, operation: 0},
		{css: 1, major: 4095, minor: 1, operation: 0},
	}
	object := newFakeThrotlCollectorBPF()
	collector := newThrotlTestCollector(t, object, 1)
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(keys[0], stableThrotlTestLane(10, 1_000)),
		throtlTestItem(keys[1], stableThrotlTestLane(20, 2_000)),
		throtlTestItem(keys[2], stableThrotlTestLane(30, 3_000)),
	}})
	_, err := collector.Update()
	require.NoError(t, err)

	// Reversing raw rows must preserve every series and its interval values.
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(keys[2], stableThrotlTestLane(31, 3_200)),
		throtlTestItem(keys[1], stableThrotlTestLane(21, 2_200)),
		throtlTestItem(keys[0], stableThrotlTestLane(11, 1_200)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 12)

	var signatures []string
	wantValues := map[string]float64{
		throtlDelayedCountName: 1,
		throtlAverageWaitName:  2,
	}
	for _, data := range metrics {
		got := inspectThrotlMetric(t, data)
		require.Equal(t, wantValues[got.name], got.value)
		labels := throtlCustomLabels(got.labels)
		signatures = append(signatures, fmt.Sprintf("%s|%s|%s|%s",
			labels["device"], labels["operation"], got.name,
			labels["scope"]))
	}
	require.ElementsMatch(t, []string{
		"4094:2|read|delayed_io_count|host",
		"4094:2|read|average_wait_milliseconds|host",
		"4095:1|read|delayed_io_count|host",
		"4095:1|read|average_wait_milliseconds|host",
		"4095:1|write|delayed_io_count|host",
		"4095:1|write|average_wait_milliseconds|host",
		"4094:2|read|delayed_io_count|other",
		"4094:2|read|average_wait_milliseconds|other",
		"4095:1|read|delayed_io_count|other",
		"4095:1|read|average_wait_milliseconds|other",
		"4095:1|write|delayed_io_count|other",
		"4095:1|write|average_wait_milliseconds|other",
	}, signatures)
}

// CSS attribution and serial-aware baseline fixtures.

func newThrotlAttributedTestCollector(
	t *testing.T,
	object *fakeThrotlCollectorBPF,
	source *ioControlAttributionTestSource,
) *throtlTracing {
	t.Helper()
	collector := newThrotlTestCollector(t, object, 1)
	collector.session.containerSource = source.read
	return collector
}

func throtlAttributionMetricValues(
	t *testing.T,
	metrics []*metric.Data,
) map[string]float64 {
	t.Helper()
	values := make(map[string]float64, len(metrics))
	for _, data := range metrics {
		got := inspectThrotlMetric(t, data)
		labels := throtlCustomLabels(got.labels)
		kind := labels["scope"]
		if strings.HasPrefix(got.name, "container_") {
			kind = "container"
		}
		values[kind+"/"+got.name] = got.value
	}
	return values
}

func requireThrotlHostAndOtherInterval(
	t *testing.T,
	metrics []*metric.Data,
	count float64,
	averageMS float64,
) {
	t.Helper()
	require.Equal(t, map[string]float64{
		"host/delayed_io_count":           count,
		"host/average_wait_milliseconds":  averageMS,
		"other/delayed_io_count":          count,
		"other/average_wait_milliseconds": averageMS,
	}, throtlAttributionMetricValues(t, metrics))
}

func requireThrotlHostAndContainerInterval(
	t *testing.T,
	metrics []*metric.Data,
	count float64,
	averageMS float64,
) {
	t.Helper()
	require.Equal(t, map[string]float64{
		"host/delayed_io_count":                         count,
		"host/average_wait_milliseconds":                averageMS,
		"container/container_delayed_io_count":          count,
		"container/container_average_wait_milliseconds": averageMS,
	}, throtlAttributionMetricValues(t, metrics))
}

// Public Other/container accounting and equal-label aggregation.

func TestThrotlAttributionMergesPublicLabelsAndPreservesRawEquation(
	t *testing.T,
) {
	startedAt := time.Unix(100, 0)
	containerA := newIOControlAttributionTestContainer(
		"container-a",
		0x11,
		startedAt,
	)
	containerB := newIOControlAttributionTestContainer(
		"container-b",
		0x22,
		startedAt.Add(time.Second),
	)
	containers := pod.BuildCssContainers(map[string]*pod.Container{
		containerA.ID: containerA,
		containerB.ID: containerB,
	}, subsystem.SubsystemBlkIO)

	keyA := throtlWaitKey{
		TD: 1, CSS: 0x11, CSSSerial: 111, Operation: 0,
	}
	keyB := throtlWaitKey{
		TD: 1, CSS: 0x22, CSSSerial: 222, Operation: 0,
	}
	otherKey := throtlWaitKey{
		TD: 1, CSS: 0x33, CSSSerial: 333, Operation: 0,
	}
	// Distinct raw serials retain independent deltas but use the same current
	// CSS mapping for public container labels.
	reusedKey := throtlWaitKey{
		TD: 1, CSS: 0x11, CSSSerial: 444, Operation: 0,
	}
	raw := map[throtlWaitKey]throtlWaitInterval{
		keyA: {
			device: "4095:1", operation: "read",
			counters: throtlWaitCounters{
				DelayedCount: 2,
				Wait10US:     400,
			},
		},
		keyB: {
			device: "4095:1", operation: "read",
			counters: throtlWaitCounters{
				DelayedCount: 3,
				Wait10US:     900,
			},
		},
		otherKey: {
			device: "4095:1", operation: "read",
			counters: throtlWaitCounters{
				DelayedCount: 7,
				Wait10US:     2_800,
			},
		},
		reusedKey: {
			device: "4095:1", operation: "read",
			counters: throtlWaitCounters{
				DelayedCount: 11,
				Wait10US:     5_500,
			},
		},
	}

	intervals := aggregateThrotlAttributedIntervals(raw, containers)
	require.Len(t, intervals.host, 1)
	require.Len(t, intervals.other, 1)
	require.Len(t, intervals.containers, 1,
		"containers with the same public labels must be one series")

	hostKey := throtlHostKey{
		device:    "4095:1",
		operation: "read",
	}
	require.Equal(t, throtlWaitCounters{
		DelayedCount: 23,
		Wait10US:     9_600,
	}, intervals.host[hostKey])
	require.Equal(t, throtlWaitCounters{
		DelayedCount: 7,
		Wait10US:     2_800,
	}, intervals.other[hostKey])

	var containerCounters throtlWaitCounters
	var representative *pod.Container
	labels, err := ioControlPublicContainerLabels(containerA)
	require.NoError(t, err)
	for key, interval := range intervals.containers {
		require.Equal(t, labels, key.labels,
			"public labels must remain part of the container grouping key")
		containerCounters = interval.counters
		representative = interval.container
	}
	require.Equal(t, throtlWaitCounters{
		DelayedCount: 16,
		Wait10US:     6_800,
	}, containerCounters)
	require.NotNil(t, representative)

	categorized := addThrotlWaitCounters(
		intervals.other[hostKey],
		containerCounters,
	)
	require.Equal(t, intervals.host[hostKey], categorized,
		"Host raw counters must equal Container plus Other")

	metrics := appendThrotlAttributedMetrics(nil, intervals)
	require.Len(t, metrics, 6)
	values := make(map[string]float64)
	for _, data := range metrics {
		got := inspectThrotlMetric(t, data)
		labels := throtlCustomLabels(got.labels)
		kind := labels["scope"]
		if got.name == "container_delayed_io_count" ||
			got.name == "container_average_wait_milliseconds" {
			kind = "container"
			require.NotContains(t, labels, "scope")
			require.Equal(t, "shared-host",
				labels["container_host"])
			require.Equal(t, "shared-name",
				labels["container_name"])
			require.Equal(t, "normal",
				labels["container_type"])
			require.Equal(t, "shared-namespace",
				labels["container_hostnamespace"])
		}
		require.Equal(t, "4095:1", labels["device"])
		require.Equal(t, "read", labels["operation"])
		values[kind+"/"+got.name] = got.value
	}
	require.Equal(t, map[string]float64{
		"host/delayed_io_count":                         23,
		"host/average_wait_milliseconds":                96.0 / 23.0,
		"other/delayed_io_count":                        7,
		"other/average_wait_milliseconds":               4,
		"container/container_delayed_io_count":          16,
		"container/container_average_wait_milliseconds": 4.25,
	}, values)
}

func TestThrotlRemovedLateCompletionNeverDeletesActiveAggregate(
	t *testing.T,
) {
	container := newIOControlAttributionTestContainer(
		"old-container",
		0x11,
		time.Unix(100, 0),
	)
	source := &ioControlAttributionTestSource{containers: map[string]*pod.Container{
		container.ID: container,
	}}
	object := newFakeThrotlCollectorBPF()
	collector := newThrotlAttributedTestCollector(t, object, source)
	key := throtlTestKey{
		css: 0x11, major: 4095, minor: 1, operation: 0,
	}
	waitKey := throtlTestRawKey(key)

	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(10, 1_000)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndContainerInterval(t, metrics, 10, 1)

	// Once the shared query stops returning the container, its completions go
	// to Other. The raw aggregate remains live while old bios can complete.
	source.containers = nil
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(11, 1_100)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherInterval(t, metrics, 1, 1)

	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(12, 1_400)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherInterval(t, metrics, 1, 3)

	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(13, 1_900)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherInterval(t, metrics, 1, 5)
	_, retained := collector.session.previous[waitKey]
	require.True(t, retained, "a live raw series must remain in the baseline")
}

func TestThrotlAttributionReadsContainersAfterRawBoundary(t *testing.T) {
	container := newIOControlAttributionTestContainer(
		"old-container",
		0x11,
		time.Unix(100, 0),
	)
	source := &ioControlAttributionTestSource{containers: map[string]*pod.Container{
		container.ID: container,
	}}
	object := newFakeThrotlCollectorBPF()
	collector := newThrotlAttributedTestCollector(t, object, source)
	key := throtlTestKey{
		css: 0x11, major: 4095, minor: 1, operation: 0,
	}

	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(10, 1_000)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndContainerInterval(t, metrics, 10, 1)

	object.dumpHook = func() {
		source.containers = nil
		object.dumpHook = nil
	}
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(11, 1_300)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherInterval(t, metrics, 1, 3)
}

// CSS identity and current mappings delimit attribution baselines.

// Reusing the CSS, blkg, and td addresses changes only the serial. The new
// cumulative row starts at zero even when the public CSS mapping is unchanged.
func TestThrotlCSSSerialKeepsCounterBaselinesSeparate(t *testing.T) {
	for _, mapped := range []bool{true, false} {
		t.Run(map[bool]string{true: "container", false: "other"}[mapped], func(t *testing.T) {
			source := &ioControlAttributionTestSource{}
			if mapped {
				container := newIOControlAttributionTestContainer(
					"container", 0x11, time.Unix(100, 0),
				)
				source.containers = map[string]*pod.Container{container.ID: container}
			}
			object := newFakeThrotlCollectorBPF()
			collector := newThrotlAttributedTestCollector(t, object, source)
			key := throtlTestKey{
				css: 0x11, blkg: 0x33, serial: 7, major: 4095, minor: 1,
			}
			requireInterval := requireThrotlHostAndOtherInterval
			if mapped {
				requireInterval = requireThrotlHostAndContainerInterval
			}
			object.setDumps(throtlTestDump{items: []bpf.MapItem{
				throtlTestItem(key, stableThrotlTestLane(10, 1_000)),
			}})
			metrics, err := collector.Update()
			require.NoError(t, err)
			requireInterval(t, metrics, 10, 1)
			oldKey := throtlTestRawKey(key)

			key.serial++
			object.setDumps(throtlTestDump{items: []bpf.MapItem{
				throtlTestItem(key, stableThrotlTestLane(1, 200)),
			}})
			metrics, err = collector.Update()
			require.NoError(t, err)
			requireInterval(t, metrics, 1, 2)
			require.NotContains(t, collector.session.previous, oldKey)
			require.Contains(t, collector.session.previous, throtlTestRawKey(key))

			object.setDumps(throtlTestDump{items: []bpf.MapItem{
				throtlTestItem(key, stableThrotlTestLane(2, 500)),
			}})
			metrics, err = collector.Update()
			require.NoError(t, err)
			requireInterval(t, metrics, 1, 3)
		})
	}
}

func TestThrotlAttributionUsesCurrentCSSMapping(t *testing.T) {
	source := &ioControlAttributionTestSource{}
	object := newFakeThrotlCollectorBPF()
	collector := newThrotlAttributedTestCollector(t, object, source)
	container := newIOControlAttributionTestContainer(
		"new-container", 0x11, time.Unix(200, 0),
	)
	source.containers = map[string]*pod.Container{container.ID: container}
	key := throtlTestKey{css: 0x11, major: 4095, minor: 1}
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(3, 900)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndContainerInterval(t, metrics, 3, 3)

	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key, stableThrotlTestLane(5, 1_700)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndContainerInterval(t, metrics, 2, 4)
}

// Container failures preserve Host and rebuild attribution baselines.

func TestThrotlContainerFailuresPreserveHostIntervals(t *testing.T) {
	for _, failure := range []string{"query", "labels", "empty"} {
		t.Run(failure, func(t *testing.T) {
			container := newIOControlAttributionTestContainer("container", 0x11, time.Unix(100, 0))
			source := &ioControlAttributionTestSource{
				containers: map[string]*pod.Container{container.ID: container},
			}
			object := newFakeThrotlCollectorBPF()
			collector := newThrotlAttributedTestCollector(t, object, source)
			key := throtlTestKey{css: 0x11, major: 4095, minor: 1}
			switch failure {
			case "query":
				// A failed source can still return data; none of it is trusted.
				source.err = errors.New("kubelet not running")
			case "labels":
				container.Labels[ioControlHostNamespaceKey] = 123
			case "empty":
				source.containers = nil
			}
			for count := uint64(3); count <= 6; count += 3 {
				object.setDumps(throtlTestDump{items: []bpf.MapItem{
					throtlTestItem(key, stableThrotlTestLane(count, count*300)),
				}})
				metrics, err := collector.Update()
				require.NoError(t, err)
				requireThrotlHostAndOtherInterval(t, metrics, 3, 3)
			}

			container.Labels[ioControlHostNamespaceKey] = "shared-namespace"
			source.err = nil
			source.containers = map[string]*pod.Container{container.ID: container}
			object.setDumps(throtlTestDump{items: []bpf.MapItem{
				throtlTestItem(key, stableThrotlTestLane(8, 2_600)),
			}})
			metrics, err := collector.Update()
			require.NoError(t, err)
			requireThrotlHostAndContainerInterval(t, metrics, 2, 4)
		})
	}
}

func TestThrotlAttributionFailuresRebuildBaseline(t *testing.T) {
	t.Run("aggregate snapshot failure", func(t *testing.T) {
		collector, object, source, key := newThrotlAttributionFailureFixture(t)
		source.containers = nil
		previous := collector.session.previous

		aggregateFailure := errors.New("aggregate dump failed")
		object.setDumps(throtlTestDump{err: aggregateFailure})
		metrics, err := collector.Update()
		require.ErrorIs(t, err, aggregateFailure)
		require.Empty(t, metrics)
		require.Equal(t, previous, collector.session.previous)

		object.setDumps(throtlTestDump{items: []bpf.MapItem{
			throtlTestItem(key,
				stableThrotlTestLane(13, 1_900)),
		}})
		metrics, err = collector.Update()
		require.ErrorIs(t, err, metric.ErrNoData)
		require.Empty(t, metrics)

		container := newIOControlAttributionTestContainer("new-container", 0x11, time.Unix(200, 0))
		source.containers = map[string]*pod.Container{container.ID: container}
		object.setDumps(throtlTestDump{items: []bpf.MapItem{
			throtlTestItem(key,
				stableThrotlTestLane(15, 2_700)),
		}})
		metrics, err = collector.Update()
		require.NoError(t, err)
		requireThrotlHostAndContainerInterval(t, metrics, 2, 4)
	})
}

func newThrotlAttributionFailureFixture(t *testing.T) (
	*throtlTracing,
	*fakeThrotlCollectorBPF,
	*ioControlAttributionTestSource,
	throtlTestKey,
) {
	t.Helper()
	container := newIOControlAttributionTestContainer(
		"old-container",
		0x11,
		time.Unix(100, 0),
	)
	source := &ioControlAttributionTestSource{
		containers: map[string]*pod.Container{container.ID: container},
	}
	object := newFakeThrotlCollectorBPF()
	collector := newThrotlAttributedTestCollector(t, object, source)
	key := throtlTestKey{
		css: 0x11, major: 4095, minor: 1, operation: 0,
	}
	object.setDumps(throtlTestDump{items: []bpf.MapItem{
		throtlTestItem(key,
			stableThrotlTestLane(10, 1_000)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndContainerInterval(t, metrics, 10, 1)
	return collector, object, source, key
}

// Compatibility and lifecycle: hooks, BPF ownership, and retirement.

// BPF ownership and startup fixtures.

const (
	throtlObjectEntries  = 4096
	throtlPendingEntries = 10240
)

type fakeThrotlBPF struct {
	bpf.BPF
	options      []bpf.AttachOption
	attachGroups [][]bpf.AttachOption
	attachErr    error
	closeErr     error
	mapID        uint32
	value        []byte
	readErr      error
	attachCalls  int
	closeCalls   int
}

func (b *fakeThrotlBPF) AttachWithOptions(options []bpf.AttachOption) error {
	b.attachCalls++
	group := append([]bpf.AttachOption(nil), options...)
	b.attachGroups = append(b.attachGroups, group)
	b.options = append(b.options, group...)
	return b.attachErr
}

func (b *fakeThrotlBPF) MapIDByName(name string) uint32 {
	switch name {
	case throtlStatusMap:
		return b.mapID
	case throtlWaitAggregateMap:
		return 2
	case throtlPendingMap:
		return 4
	case throtlTDMap:
		return 5
	default:
		return 0
	}
}

func (b *fakeThrotlBPF) ReadMap(uint32, []byte) ([]byte, error) {
	return b.value, b.readErr
}

func testThrotlHooks(
	diskProfile bool,
) *throtlHooks {
	return &throtlHooks{
		diskProfile:  diskProfile,
		trackedRange: symbol.KsymbolRange{Start: 0x1000, End: 0x1100},
		ignoredRange: symbol.KsymbolRange{Start: 0x2000, End: 0x2100},
	}
}

func (b *fakeThrotlBPF) Close() error {
	b.closeCalls++
	return b.closeErr
}

func (*fakeThrotlBPF) DetachOnContextDone(context.Context, context.CancelFunc) {}

type orderedThrotlBPF struct {
	fakeThrotlBPF
	events *[]string
}

func (b *orderedThrotlBPF) AttachWithOptions(
	options []bpf.AttachOption,
) error {
	for _, option := range options {
		*b.events = append(*b.events, "attach:"+option.ProgramName)
	}
	return b.fakeThrotlBPF.AttachWithOptions(options)
}

func (b *orderedThrotlBPF) Close() error {
	*b.events = append(*b.events, "object-close")
	return b.fakeThrotlBPF.Close()
}

func emptyThrotlContainerSource() (map[string]*pod.Container, error) {
	return nil, nil
}

type blockingThrotlBPF struct {
	fakeThrotlBPF
	mu             sync.Mutex
	dumpStarted    chan struct{}
	releaseDump    chan struct{}
	sessionStarted chan struct{}
	closed         chan struct{}
	dumpOnce       sync.Once
	startOnce      sync.Once
	closeOnce      sync.Once
	closedFlag     bool
	readAfterClose bool
	detach         context.CancelFunc
}

func newBlockingThrotlBPF() *blockingThrotlBPF {
	return &blockingThrotlBPF{
		fakeThrotlBPF: fakeThrotlBPF{
			mapID: 7,
			value: make([]byte, binary.Size(throtlStatus{})),
		},
		dumpStarted:    make(chan struct{}),
		releaseDump:    make(chan struct{}),
		sessionStarted: make(chan struct{}),
		closed:         make(chan struct{}),
	}
}

func (b *blockingThrotlBPF) markRead() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closedFlag {
		b.readAfterClose = true
	}
}

func (b *blockingThrotlBPF) DumpMapByName(
	name string,
) ([]bpf.MapItem, error) {
	b.markRead()
	if name == throtlWaitAggregateMap {
		b.dumpOnce.Do(func() { close(b.dumpStarted) })
		<-b.releaseDump
		return nil, nil
	}
	if name == throtlTDMap || name == throtlPendingMap {
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected map %q", name)
}

func (b *blockingThrotlBPF) DumpMapBatch(
	mapID uint32,
) ([]bpf.MapItem, error) {
	if mapID != b.MapIDByName(throtlWaitAggregateMap) {
		return nil, fmt.Errorf("unexpected batch map ID %d", mapID)
	}
	return b.DumpMapByName(throtlWaitAggregateMap)
}

func (b *blockingThrotlBPF) MapIDByName(name string) uint32 {
	b.markRead()
	return b.fakeThrotlBPF.MapIDByName(name)
}

func (b *blockingThrotlBPF) ReadMap(
	mapID uint32,
	key []byte,
) ([]byte, error) {
	b.markRead()
	return b.fakeThrotlBPF.ReadMap(mapID, key)
}

func (b *blockingThrotlBPF) DetachOnContextDone(
	_ context.Context,
	cancel context.CancelFunc,
) {
	b.detach = cancel
	b.startOnce.Do(func() { close(b.sessionStarted) })
}

func (b *blockingThrotlBPF) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeOnce.Do(func() {
		b.closedFlag = true
		b.closeCalls++
		close(b.closed)
	})
	return b.closeErr
}

func (b *blockingThrotlBPF) closeState() (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeCalls, b.readAfterClose
}

// Object contracts, status decoding, and required attachment paths.

func TestThrotlObjectContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)

	object := filepath.Join(filepath.Dir(file), "..", "..", "bpf",
		"throtl_tracing.o")
	spec, err := ebpf.LoadCollectionSpec(object)
	require.NoError(t, err)

	wantPrograms := []string{
		"kprobe_blk_throtl_exit_legacy",
		"kprobe_blk_throtl_exit_mainline",
		"kprobe_throtl_pd_free",
		"kretprobe_throtl_pop_queued",
		"kprobe_throtl_pop_queued",
		"kprobe_throtl_add_bio_tg",
	}
	programNames := make([]string, 0, len(spec.Programs))
	for name := range spec.Programs {
		programNames = append(programNames, name)
	}
	require.ElementsMatch(t, wantPrograms, programNames)

	maps := []struct {
		name       string
		typeID     ebpf.MapType
		keySize    uint32
		valueSize  uint32
		maxEntries uint32
		flags      uint32
	}{
		{
			name: "throtl_td_map", typeID: ebpf.Hash, keySize: 8,
			valueSize: 16, maxEntries: throtlObjectEntries,
		},
		{
			name: "throtl_blkg_owner_map", typeID: ebpf.Hash, keySize: 8,
			valueSize: 24, maxEntries: throtlObjectEntries,
		},
		{
			name: "throtl_pending_map", typeID: ebpf.Hash, keySize: 16,
			valueSize: 8, maxEntries: throtlPendingEntries,
		},
		{
			name: "throtl_pop_call_map", typeID: ebpf.Hash, keySize: 8,
			valueSize: 8, maxEntries: throtlObjectEntries,
		},
		{
			name: "throtl_wait_agg_map", typeID: ebpf.PerCPUHash, keySize: 40,
			valueSize: 8, maxEntries: throtlObjectEntries * 2,
			flags: unix.BPF_F_NO_PREALLOC,
		},
		{
			name: "throtl_stat_map", typeID: ebpf.Array, keySize: 4,
			valueSize: 8, maxEntries: 1,
		},
	}
	for _, expected := range maps {
		actual, exists := spec.Maps[expected.name]
		require.True(t, exists, "map %s", expected.name)
		require.Equal(t, expected.typeID, actual.Type, "map %s type",
			expected.name)
		require.Equal(t, expected.maxEntries, actual.MaxEntries,
			"map %s capacity", expected.name)
		require.Equal(t, expected.keySize, actual.KeySize,
			"map %s key size", expected.name)
		require.Equal(t, expected.valueSize, actual.ValueSize,
			"map %s value size", expected.name)
		require.Equal(t, expected.flags, actual.Flags,
			"map %s flags", expected.name)
	}
	require.NoError(t, spec.RewriteConstants(map[string]any{
		"throtl_tracked_caller_start": uint64(1),
		"throtl_tracked_caller_end":   uint64(2),
		"throtl_ignored_caller_start": uint64(3),
		"throtl_ignored_caller_end":   uint64(4),
	}))
}

func TestThrotlAttachHooks(t *testing.T) {
	for _, diskProfile := range []bool{false, true} {
		hooks := throtlHooks{
			diskProfile: diskProfile,
		}
		exitProgram := "kprobe_blk_throtl_exit_legacy"
		if diskProfile {
			exitProgram = "kprobe_blk_throtl_exit_mainline"
		}
		want := []bpf.AttachOption{
			{ProgramName: exitProgram, Symbol: "blk_throtl_exit"},
			{ProgramName: "kprobe_throtl_pd_free", Symbol: "throtl_pd_free"},
			{ProgramName: "kretprobe_throtl_pop_queued", Symbol: "throtl_pop_queued"},
			{ProgramName: "kprobe_throtl_pop_queued", Symbol: "throtl_pop_queued"},
			{ProgramName: "kprobe_throtl_add_bio_tg", Symbol: "throtl_add_bio_tg"},
		}

		object := &fakeThrotlBPF{}
		require.NoError(t, attachThrotlHooks(object, &hooks))
		require.Equal(t, want, object.options)
		require.Equal(t, [][]bpf.AttachOption{want}, object.attachGroups)
		require.Equal(t, 1, object.attachCalls)

		failing := &fakeThrotlBPF{attachErr: errors.New("attach failed")}
		err := attachThrotlHooks(failing, &hooks)
		require.ErrorContains(t, err, "attach blk-throttle hooks")
		require.ErrorIs(t, err, failing.attachErr)
		require.Equal(t, want, failing.options)
		require.Equal(t, 1, failing.attachCalls)
	}
}

func TestThrotlStatusMapABI(t *testing.T) {
	want := throtlStatus{
		Reason: throtlFailurePendingInsert,
		Errno:  -int32(unix.E2BIG),
	}
	data := encodeThrotlTestData(t, want)
	require.Len(t, data, 8)
	packed := uint64(2)<<32 | uint64(uint32(want.Errno))
	require.Equal(t, packed, binary.LittleEndian.Uint64(data))
	var got uint64
	require.NoError(t, decodeBPFMapData(data, &got))
	require.Equal(t, packed, got)

	for _, size := range []int{0, len(data) - 1, len(data) + 1} {
		require.Error(t, decodeBPFMapData(make([]byte, size), &got))
	}
	require.Error(t, decodeBPFMapData(data, nil))
}

func TestThrotlSessionReadStatus(t *testing.T) {
	want := throtlStatus{Reason: throtlFailureLifecycle, Errno: -int32(unix.EIO)}
	tests := []struct {
		name    string
		object  fakeThrotlBPF
		wantErr string
	}{
		{name: "valid", object: fakeThrotlBPF{
			mapID: 7,
			value: encodeThrotlTestData(t, want),
		}},
		{name: "missing map", wantErr: "is unavailable"},
		{name: "read failure", object: fakeThrotlBPF{
			mapID:   7,
			readErr: errors.New("read failed"),
		}, wantErr: "read failed"},
		{name: "short value", object: fakeThrotlBPF{
			mapID: 7,
			value: make([]byte, binary.Size(throtlStatus{})-1),
		}, wantErr: "data size"},
		{name: "long value", object: fakeThrotlBPF{
			mapID: 7,
			value: make([]byte, binary.Size(throtlStatus{})+1),
		}, wantErr: "data size"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := throtlSession{object: &test.object}
			got, err := session.readStatus()
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

// Startup, permanent-stop, retry, and in-flight cancellation boundaries.

func TestThrotlStartWithHooks(t *testing.T) {
	hooks := testThrotlHooks(false)
	wantConstants := map[string]any{
		"throtl_tracked_caller_start": uint64(0x1000),
		"throtl_tracked_caller_end":   uint64(0x1100),
		"throtl_ignored_caller_start": uint64(0x2000),
		"throtl_ignored_caller_end":   uint64(0x2100),
	}
	wantOptions := []bpf.AttachOption{
		{ProgramName: "kprobe_blk_throtl_exit_legacy", Symbol: "blk_throtl_exit"},
		{ProgramName: "kprobe_throtl_pd_free", Symbol: "throtl_pd_free"},
		{ProgramName: "kretprobe_throtl_pop_queued", Symbol: "throtl_pop_queued"},
		{ProgramName: "kprobe_throtl_pop_queued", Symbol: "throtl_pop_queued"},
		{ProgramName: "kprobe_throtl_add_bio_tg", Symbol: "throtl_add_bio_tg"},
	}

	t.Run("loads rodata and attaches the complete group", func(t *testing.T) {
		object := &fakeThrotlBPF{}
		var gotConstants map[string]any
		load := func(_ string, constants map[string]any) (bpf.BPF, error) {
			gotConstants = constants
			return object, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		tracing := &throtlTracing{}
		require.NoError(t, tracing.startWithAttribution(
			ctx,
			load,
			hooks,
			1,
			emptyThrotlContainerSource,
		))
		require.Equal(t, wantConstants, gotConstants)
		require.Equal(t, wantOptions, object.options)
		require.Equal(t, 1, object.attachCalls)
		require.Equal(t, 1, object.closeCalls)
		require.Nil(t, tracing.session)
	})

	t.Run("closes object after group attach failure", func(t *testing.T) {
		object := &fakeThrotlBPF{attachErr: errors.New("attach failed")}
		load := func(_ string, constants map[string]any) (bpf.BPF, error) {
			require.Equal(t, wantConstants, constants)
			return object, nil
		}

		tracing := &throtlTracing{}
		err := tracing.startWithAttribution(
			t.Context(),
			load,
			hooks,
			1,
			emptyThrotlContainerSource,
		)
		require.ErrorContains(t, err, "attach blk-throttle hooks")
		require.Equal(t, wantOptions, object.options)
		require.Equal(t, 1, object.attachCalls)
		require.Equal(t, 1, object.closeCalls)
		require.Nil(t, tracing.session)
	})
}

// A terminal status read must release Start only after the BPF object is closed.
func TestThrotlFatalUpdateStopsStart(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   throtlStatus
		reason   string
		closeErr error
	}{
		{
			name: "duplicate pending", reason: "duplicate admission",
			status: throtlStatus{Reason: throtlFailurePendingCollision, Errno: -int32(unix.EEXIST)},
		},
		{
			name: "unknown caller", reason: "unrecognized throtl_add_bio_tg caller",
			status: throtlStatus{Reason: throtlFailureUnknownCaller},
		},
		{
			name: "invalid lifecycle", reason: "td state",
			status: throtlStatus{Reason: throtlFailureLifecycle},
		},
		{
			name: "map capacity", reason: "capacity exhausted (10240 entries)",
			status: throtlStatus{Reason: throtlFailurePendingInsert, Errno: -int32(unix.E2BIG)},
		},
		{
			name: "pending insertion", reason: "pending insertion failed: helper returned -12",
			status: throtlStatus{Reason: throtlFailurePendingInsert, Errno: -int32(unix.ENOMEM)},
		},
		{
			name: "pending deletion", reason: "pending deletion failed: helper returned -5",
			status: throtlStatus{Reason: throtlFailurePendingDelete, Errno: -int32(unix.EIO)},
		},
		{
			name: "owner creation", reason: "blkg owner",
			status: throtlStatus{Reason: throtlFailureOwner, Errno: -int32(unix.E2BIG)},
		},
		{
			name: "aggregate allocation", reason: "helper returned -12",
			status: throtlStatus{Reason: throtlFailureAggregate, Errno: -int32(unix.ENOMEM)},
		},
		{
			name: "pop state", reason: "helper returned -17",
			status: throtlStatus{Reason: throtlFailurePopState, Errno: -int32(unix.EEXIST)},
		},
		{
			name: "time rollback", reason: "wait end precedes",
			status: throtlStatus{Reason: throtlFailureTimeRollback},
		},
		{
			name: "unknown stop reason", reason: "unknown BPF stop reason 99",
			status: throtlStatus{Reason: 99},
		},
		{
			name: "cleanup failure", reason: "duplicate admission",
			status:   throtlStatus{Reason: throtlFailurePendingCollision, Errno: -int32(unix.EEXIST)},
			closeErr: errors.New("close failed"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := newBlockingThrotlBPF()
			object.value = encodeThrotlTestData(t, test.status)
			object.closeErr = test.closeErr
			close(object.releaseDump)
			collector := &throtlTracing{}
			startErrors := make(chan error, 1)
			ctx, cancelRun := context.WithCancel(t.Context())
			t.Cleanup(cancelRun)
			go func() {
				startErrors <- collector.startWithAttribution(ctx,
					func(string, map[string]any) (bpf.BPF, error) { return object, nil },
					testThrotlHooks(false), 1, emptyThrotlContainerSource)
			}()
			select {
			case <-object.sessionStarted:
			case <-time.After(time.Second):
				t.Fatal("blk_throtl session was not published")
			}
			metrics, err := collector.Update()
			require.Nil(t, metrics)
			require.ErrorIs(t, err, types.ErrTracingStopped)
			require.ErrorContains(t, err, test.reason)
			if test.status.Errno != 0 {
				require.ErrorContains(t, err, fmt.Sprintf("helper returned %d", test.status.Errno))
			}
			if test.status.Reason != throtlFailurePendingInsert || test.status.Errno != -int32(unix.E2BIG) {
				require.NotContains(t, err.Error(), "capacity exhausted")
			}
			select {
			case err := <-startErrors:
				require.ErrorIs(t, err, types.ErrTracingStopped)
				require.ErrorContains(t, err, test.reason)
				if test.closeErr != nil {
					require.ErrorIs(t, err, test.closeErr)
				}
			case <-time.After(time.Second):
				t.Fatal("Update did not stop blk_throtl Start")
			}
			closes, readAfterClose := object.closeState()
			require.Equal(t, 1, closes)
			require.False(t, readAfterClose)
			collector.mu.Lock()
			session := collector.session
			collector.mu.Unlock()
			require.Nil(t, session)
			metrics, err = collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Nil(t, metrics)
		})
	}
}

func TestThrotlBackendDetachAllowsRetry(t *testing.T) {
	object := newBlockingThrotlBPF()
	collector := &throtlTracing{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- collector.startWithAttribution(ctx,
			func(string, map[string]any) (bpf.BPF, error) { return object, nil },
			testThrotlHooks(false), 1, emptyThrotlContainerSource)
	}()
	select {
	case <-object.sessionStarted:
	case <-time.After(time.Second):
		t.Fatal("blk_throtl session was not published")
	}
	object.detach()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("backend detach did not release blk_throtl Start")
	}
	closes, readAfterClose := object.closeState()
	require.Equal(t, 1, closes)
	require.False(t, readAfterClose)
}

func TestThrotlStartIgnoresContainerDiscovery(
	t *testing.T,
) {
	hooks := testThrotlHooks(false)
	var events []string
	object := &orderedThrotlBPF{events: &events}
	load := func(string, map[string]any) (bpf.BPF, error) {
		events = append(events, "load")
		return object, nil
	}
	containerSource := func() (map[string]*pod.Container, error) {
		events = append(events, "container-snapshot")
		return nil, errors.New("kubelet not running")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tracing := &throtlTracing{}
	require.NoError(t, tracing.startWithAttribution(
		ctx,
		load,
		hooks,
		1,
		containerSource,
	))
	require.Equal(t, []string{
		"load",
		"attach:kprobe_blk_throtl_exit_legacy",
		"attach:kprobe_throtl_pd_free",
		"attach:kretprobe_throtl_pop_queued",
		"attach:kprobe_throtl_pop_queued",
		"attach:kprobe_throtl_add_bio_tg",
		"object-close",
	}, events)
	require.Nil(t, tracing.session)
}

func TestThrotlIntervalCancelWaitsForInFlightCollection(t *testing.T) {
	object := newBlockingThrotlBPF()
	load := func(string, map[string]any) (bpf.BPF, error) {
		return object, nil
	}
	hooks := testThrotlHooks(false)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tracing := &throtlTracing{}
	startDone := make(chan error, 1)
	go func() {
		startDone <- tracing.startWithAttribution(
			ctx,
			load,
			hooks,
			1,
			emptyThrotlContainerSource,
		)
	}()

	select {
	case <-object.sessionStarted:
	case <-time.After(time.Second):
		t.Fatal("Start did not publish the blk_throtl session")
	}
	tracing.mu.Lock()
	session := tracing.session
	tracing.mu.Unlock()
	require.NotNil(t, session)
	baseline := session.previous

	updateDone := make(chan error, 1)
	go func() {
		_, err := tracing.Update()
		updateDone <- err
	}()
	select {
	case <-object.dumpStarted:
	case <-time.After(time.Second):
		t.Fatal("Update did not enter the aggregate dump")
	}

	// Start must not withdraw the session or close its BPF object while Update
	// owns the shared lifecycle lock and is still reading the map.
	cancel()
	select {
	case <-session.breaker.Done():
	case <-time.After(time.Second):
		t.Fatal("session breaker did not observe cancellation")
	}
	require.Never(t, func() bool {
		select {
		case <-object.closed:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, 10*time.Millisecond)
	require.Never(t, func() bool {
		select {
		case <-startDone:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, 10*time.Millisecond)
	closeCalls, readAfterClose := object.closeState()
	require.Zero(t, closeCalls)
	require.False(t, readAfterClose)

	close(object.releaseDump)
	select {
	case err := <-updateDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Update did not leave the released aggregate dump")
	}
	require.Equal(t, baseline, session.previous,
		"a canceled generation must not commit its warm-up baseline")

	select {
	case err := <-startDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Start did not withdraw and close the canceled session")
	}
	select {
	case <-object.closed:
	default:
		t.Fatal("BPF object was not closed after collection released the lock")
	}
	closeCalls, readAfterClose = object.closeState()
	require.Equal(t, 1, closeCalls)
	require.False(t, readAfterClose)
	tracing.mu.Lock()
	require.Nil(t, tracing.session)
	tracing.mu.Unlock()
}

// Opt-in production attach qualification.

// Run with TEST_INTEGRATION=true after building bpf/throtl_tracing.o.
func TestThrotlStartAttachSmoke(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to run the real blk_throtl attach smoke")
	}
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join(filepath.Dir(file), "..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = oldDir })

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tracing := &throtlTracing{}
	require.NoError(t, tracing.Start(ctx))
	require.Nil(t, tracing.session)
}

func encodeThrotlTestData(t *testing.T, value any) []byte {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, binary.Write(&data, binary.LittleEndian, value))
	return data.Bytes()
}

// Runtime hook selection and incomplete-profile rejection.

func TestResolveThrotlHooks(t *testing.T) {
	tests := []struct {
		name    string
		profile symbol.KsymbolProfile
		want    throtlHooks
	}{
		{
			name: "disk profile uses modern exit",
			profile: throtlHookProfileForTest(map[string]symbol.KsymbolRange{
				"__blk_throtl_bio": {Start: 0x1000, End: 0x1100},
				"blk_throtl_bio":   {Start: 0x1800, End: 0x1900},
			}),
			want: throtlHooks{
				diskProfile:  true,
				trackedRange: symbol.KsymbolRange{Start: 0x1000, End: 0x1100},
				ignoredRange: symbol.KsymbolRange{Start: 0x2000, End: 0x2100},
			},
		},
		{
			name: "legacy tracked caller is the fallback",
			profile: throtlHookProfileForTest(map[string]symbol.KsymbolRange{
				"blk_throtl_bio": {Start: 0x1000, End: 0x1100},
			}),
			want: throtlHooks{
				trackedRange: symbol.KsymbolRange{Start: 0x1000, End: 0x1100},
				ignoredRange: symbol.KsymbolRange{Start: 0x2000, End: 0x2100},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.want.diskProfile {
				delete(test.profile.Addresses, throtlLegacyExitMarker)
				test.profile.Addresses[throtlMainlineExitMarker] = 0x6000
			}
			calls := 0
			got, err := resolveThrotlHooks(
				func(addresses, ranges []string) (symbol.KsymbolProfile, error) {
					calls++
					require.ElementsMatch(t, []string{
						"throtl_add_bio_tg",
						"throtl_pop_queued",
						"throtl_pd_free",
						"blk_throtl_exit",
						"blkcg_exit_queue",
						"blkcg_exit_disk",
					}, addresses)
					require.ElementsMatch(t, []string{
						"__blk_throtl_bio",
						"blk_throtl_bio",
						"tg_dispatch_one_bio",
					}, ranges)
					return test.profile, nil
				},
			)
			require.NoError(t, err)
			require.Equal(t, &test.want, got)
			require.Equal(t, 1, calls,
				"one hook profile must come from one kallsyms scan")
		})
	}
}

func TestResolveThrotlHooksRejectsIncompleteProfile(t *testing.T) {
	tests := []struct {
		name        string
		missing     string
		wantInError string
	}{
		{name: "tracked caller", missing: "blk_throtl_bio", wantInError: "tracked caller"},
		{name: "ignored caller", missing: "tg_dispatch_one_bio", wantInError: "ignored caller"},
		{name: "add hook", missing: "throtl_add_bio_tg", wantInError: "throtl_add_bio_tg"},
		{name: "pop hook", missing: "throtl_pop_queued", wantInError: "throtl_pop_queued"},
		{name: "pd free hook", missing: "throtl_pd_free", wantInError: "throtl_pd_free"},
		{name: "exit hook", missing: "blk_throtl_exit", wantInError: "blk_throtl_exit"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := throtlHookProfileForTest(map[string]symbol.KsymbolRange{
				"blk_throtl_bio": {Start: 0x1000, End: 0x1100},
			})
			delete(profile.Addresses, test.missing)
			delete(profile.Ranges, test.missing)
			got, err := resolveThrotlHooks(
				func([]string, []string) (symbol.KsymbolProfile, error) {
					return profile, nil
				},
			)
			require.ErrorContains(t, err, test.wantInError)
			require.Nil(t, got)
		})
	}
}

func TestResolveThrotlHooksRejectsAmbiguousExitProfile(
	t *testing.T,
) {
	for _, mutate := range []func(symbol.KsymbolProfile){
		func(profile symbol.KsymbolProfile) {
			delete(profile.Addresses, throtlLegacyExitMarker)
		},
		func(profile symbol.KsymbolProfile) {
			profile.Addresses[throtlMainlineExitMarker] = 0x6000
		},
	} {
		profile := throtlHookProfileForTest(map[string]symbol.KsymbolRange{
			"blk_throtl_bio": {Start: 0x1000, End: 0x1100},
		})
		mutate(profile)
		got, err := resolveThrotlHooks(
			func([]string, []string) (symbol.KsymbolProfile, error) {
				return profile, nil
			},
		)
		require.ErrorContains(t, err, "exactly one")
		require.Nil(t, got)
	}
}

func TestResolveThrotlHooksRejectsOverlappingCallers(t *testing.T) {
	profile := throtlHookProfileForTest(map[string]symbol.KsymbolRange{
		"blk_throtl_bio": {Start: 0x1000, End: 0x1100},
	})
	profile.Ranges["tg_dispatch_one_bio"] = symbol.KsymbolRange{
		Start: 0x1080,
		End:   0x1180,
	}
	got, err := resolveThrotlHooks(
		func([]string, []string) (symbol.KsymbolProfile, error) {
			return profile, nil
		},
	)
	require.Error(t, err)
	require.Nil(t, got)
}

func TestResolveThrotlHooksPropagatesProfileFailure(
	t *testing.T,
) {
	failure := errors.New("scan failed")
	got, err := resolveThrotlHooks(
		func([]string, []string) (symbol.KsymbolProfile, error) {
			return symbol.KsymbolProfile{}, failure
		},
	)
	require.ErrorIs(t, err, failure)
	require.Nil(t, got)
}

func TestLoadThrotlHooksRetriesSymbolReadFailure(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	path := procfs.Path("kallsyms")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	hooks, err := loadThrotlHooks()
	require.Nil(t, hooks)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NotErrorIs(t, err, types.ErrNotSupported,
		"a symbol file read failure must remain retryable")

	// A successful read with missing required hooks proves unsupported.
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	hooks, err = loadThrotlHooks()
	require.Nil(t, hooks)
	require.ErrorIs(t, err, types.ErrNotSupported)

	const kallsyms = `0000000000001000 t blk_throtl_bio
0000000000001100 t tracked_end
0000000000002000 t tg_dispatch_one_bio
0000000000002100 t ignored_end
0000000000003000 t throtl_add_bio_tg
0000000000004000 t throtl_pop_queued
0000000000005000 t throtl_pd_free
0000000000006000 t blk_throtl_exit
0000000000007000 t blkcg_exit_queue
`
	require.NoError(t, os.WriteFile(path, []byte(kallsyms), 0o600))
	hooks, err = loadThrotlHooks()
	require.NoError(t, err)
	require.NotNil(t, hooks)
}

func throtlHookProfileForTest(
	extra map[string]symbol.KsymbolRange,
) symbol.KsymbolProfile {
	profile := symbol.KsymbolProfile{
		Addresses: map[string]uint64{
			"throtl_add_bio_tg": 0x3000,
			"throtl_pop_queued": 0x4000,
			"throtl_pd_free":    0x5000,
			"blk_throtl_exit":   0x6000,
			"blkcg_exit_queue":  0x7000,
		},
		Ranges: map[string]symbol.KsymbolRange{
			"tg_dispatch_one_bio": {Start: 0x2000, End: 0x2100},
		},
	}
	for name, value := range extra {
		profile.Ranges[name] = value
	}
	return profile
}

// Retired TD cleanup preserves live-last ordering and retry isolation.

type statefulThrotlCollectorBPF struct {
	bpf.BPF
	maps           map[string][]bpf.MapItem
	mapIDs         map[string]uint32
	mapNames       map[uint32]string
	statusValue    []byte
	deleteFailures map[string]error
	deleteEvents   []string
	dumpCalls      map[string]int
	dumpResult     func(string, []bpf.MapItem) ([]bpf.MapItem, error)
	afterDelete    map[string]func()
}

func newStatefulThrotlCollectorBPF() *statefulThrotlCollectorBPF {
	mapIDs := map[string]uint32{
		throtlStatusMap:        1,
		throtlWaitAggregateMap: 2,
		throtlBLKGOwnerMap:     3,
		throtlPendingMap:       4,
		throtlTDMap:            5,
	}
	mapNames := make(map[uint32]string, len(mapIDs))
	for name, id := range mapIDs {
		mapNames[id] = name
	}
	return &statefulThrotlCollectorBPF{
		maps: map[string][]bpf.MapItem{
			throtlWaitAggregateMap: nil,
			throtlBLKGOwnerMap:     nil,
			throtlPendingMap:       nil,
			throtlTDMap:            nil,
		},
		mapIDs:         mapIDs,
		mapNames:       mapNames,
		statusValue:    make([]byte, binary.Size(throtlStatus{})),
		deleteFailures: make(map[string]error),
		dumpCalls:      make(map[string]int),
		afterDelete:    make(map[string]func()),
	}
}

func (b *statefulThrotlCollectorBPF) DumpMapByName(
	name string,
) ([]bpf.MapItem, error) {
	items, exists := b.maps[name]
	if !exists {
		return nil, fmt.Errorf("unexpected map %q", name)
	}
	b.dumpCalls[name]++
	if b.dumpResult != nil {
		return b.dumpResult(name, cloneThrotlMapItems(items))
	}
	return cloneThrotlMapItems(items), nil
}

func (b *statefulThrotlCollectorBPF) DumpMapBatch(
	mapID uint32,
) ([]bpf.MapItem, error) {
	if mapID != b.MapIDByName(throtlWaitAggregateMap) {
		return nil, fmt.Errorf("unexpected batch map ID %d", mapID)
	}
	return b.DumpMapByName(throtlWaitAggregateMap)
}

func (b *statefulThrotlCollectorBPF) MapIDByName(name string) uint32 {
	return b.mapIDs[name]
}

func (b *statefulThrotlCollectorBPF) ReadMap(
	mapID uint32,
	key []byte,
) ([]byte, error) {
	if b.mapNames[mapID] != throtlStatusMap {
		return nil, fmt.Errorf("unexpected status map ID %d", mapID)
	}
	if len(key) != 4 || binary.LittleEndian.Uint32(key) != 0 {
		return nil, fmt.Errorf("unexpected status key %v", key)
	}
	return append([]byte(nil), b.statusValue...), nil
}

func (b *statefulThrotlCollectorBPF) DeleteMapItems(
	mapID uint32,
	keys [][]byte,
) error {
	name := b.mapNames[mapID]
	if name == "" {
		return fmt.Errorf("unexpected map ID %d", mapID)
	}
	b.deleteEvents = append(b.deleteEvents, name)
	if err := b.deleteFailures[name]; err != nil {
		return err
	}
	deleted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		deleted[string(key)] = struct{}{}
	}
	items := b.maps[name]
	kept := items[:0]
	for _, item := range items {
		if _, exists := deleted[string(item.Key)]; exists {
			continue
		}
		kept = append(kept, item)
	}
	b.maps[name] = kept
	if hook := b.afterDelete[name]; hook != nil {
		hook()
	}
	return nil
}

func cloneThrotlMapItems(items []bpf.MapItem) []bpf.MapItem {
	clone := make([]bpf.MapItem, 0, len(items))
	for _, item := range items {
		clone = append(clone, bpf.MapItem{
			Key:   append([]byte(nil), item.Key...),
			Value: append([]byte(nil), item.Value...),
		})
	}
	return clone
}

func TestThrotlRetiredTDCleanupIsIsolatedAndOrdered(t *testing.T) {
	object, collector, active, retired := newThrotlRetirementTestState(t)
	// Cleanup removes both operations while preserving another td's pending IO.
	retiredRead := retired
	retiredRead.operation = 0
	object.maps[throtlWaitAggregateMap] = append(
		object.maps[throtlWaitAggregateMap],
		throtlTestItem(retiredRead, stableThrotlTestLane(10, 2_000)),
	)
	activePending := throtlPendingKey{TD: active.td, Bio: 0x6000}
	object.maps[throtlPendingMap] = append(
		object.maps[throtlPendingMap],
		throtlObjectTestItem(activePending, uint64(456)),
	)

	metrics, err := collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics, "8:1", "read", 3, 1)
	require.Equal(t, []string{
		throtlWaitAggregateMap,
		throtlPendingMap,
		throtlTDMap,
	}, object.deleteEvents)

	require.Len(t, object.maps[throtlWaitAggregateMap], 1)
	require.Equal(t, throtlTestKeyBytes(active),
		object.maps[throtlWaitAggregateMap][0].Key)
	require.Len(t, object.maps[throtlBLKGOwnerMap], 2,
		"blkg lifecycle owns owner deletion")
	require.Len(t, object.maps[throtlPendingMap], 1)
	require.Equal(t, encodeThrotlTestDataValue(activePending),
		object.maps[throtlPendingMap][0].Key)
	require.Len(t, object.maps[throtlTDMap], 1)
	require.Equal(t, encodeThrotlTestDataValue(active.td),
		object.maps[throtlTDMap][0].Key)
	require.Contains(t, collector.session.previous,
		throtlTestRawKey(active))
	require.NotContains(t, collector.session.previous,
		throtlTestRawKey(retired))
}

func TestThrotlRetiredTDCleanupRetriesWithoutBlockingActiveMetrics(
	t *testing.T,
) {
	for _, failedMap := range []string{
		throtlWaitAggregateMap,
		throtlPendingMap,
		throtlTDMap,
	} {
		t.Run(failedMap, func(t *testing.T) {
			object, collector, active, retired := newThrotlRetirementTestState(t)
			object.deleteFailures[failedMap] = errors.New("delete failed")

			metrics, err := collector.Update()
			require.NoError(t, err)
			requireThrotlHostAndOtherMetrics(
				t, metrics, "8:1", "read", 3, 1,
			)
			require.True(t, throtlTestTDExists(
				object.maps[throtlTDMap], retired.td,
			), "RETIRED must remain until every subordinate map is clean")
			require.Equal(t, uint64(8),
				collector.session.previous[throtlTestRawKey(active)].counters[0].DelayedCount,
				"a deferred cleanup must not block an unrelated active baseline",
			)

			delete(object.deleteFailures, failedMap)
			setThrotlTestAggregate(object, active,
				stableThrotlTestLane(10, 1_000))
			metrics, err = collector.Update()
			require.NoError(t, err)
			requireThrotlHostAndOtherMetrics(
				t, metrics, "8:1", "read", 2, 1,
			)
			require.False(t, throtlTestTDExists(
				object.maps[throtlTDMap], retired.td,
			))
		})
	}
}

func TestThrotlRetiredTDCleanupBatchesMapsAndDefersBusyTD(t *testing.T) {
	object, collector, active, first := newThrotlRetirementTestState(t)
	second := normalizedThrotlTestKey(throtlTestKey{
		td: 0x6000, blkg: 0x7000, css: 0xc3, serial: 33, operation: 0,
	})
	object.maps[throtlWaitAggregateMap] = append(
		object.maps[throtlWaitAggregateMap],
		throtlTestItem(second, stableThrotlTestLane(30, 6_000)),
	)
	object.maps[throtlBLKGOwnerMap] = append(
		object.maps[throtlBLKGOwnerMap],
		throtlObjectTestItem(second.blkg, throtlTestBLKGOwner{
			TD: second.td, CSS: 0xc3, CSSSerial: second.serial,
		}),
	)
	object.maps[throtlTDMap] = append(
		object.maps[throtlTDMap],
		throtlObjectTestItem(second.td, throtlTDValue{
			State: throtlTDRetired, Major: 8, Minor: 3,
		}),
	)
	object.afterDelete[throtlPendingMap] = func() {
		delete(object.afterDelete, throtlPendingMap)
		object.maps[throtlPendingMap] = append(
			object.maps[throtlPendingMap],
			throtlObjectTestItem(
				throtlPendingKey{TD: second.td, Bio: 0x8000},
				uint64(456),
			),
		)
	}

	metrics, err := collector.Update()
	require.NoError(t, err)
	requireThrotlHostAndOtherMetrics(t, metrics, "8:1", "read", 3, 1)
	require.False(t, throtlTestTDExists(
		object.maps[throtlTDMap], first.td,
	))
	require.True(t, throtlTestTDExists(
		object.maps[throtlTDMap], second.td,
	), "a td with a late pending row must remain RETIRED")
	require.Equal(t, map[string]int{
		throtlWaitAggregateMap: 2,
		throtlTDMap:            1,
		throtlPendingMap:       2,
	}, object.dumpCalls,
		"aggregate cleanup reuses the raw snapshot and scans stay batched")
	require.Equal(t, []string{
		throtlWaitAggregateMap,
		throtlPendingMap,
		throtlTDMap,
	}, object.deleteEvents)
	require.Contains(t, collector.session.previous,
		throtlTestRawKey(active))
}

func TestThrotlCleanupScanFailuresPreserveActiveIntervals(t *testing.T) {
	for _, stage := range []struct {
		name    string
		mapName string
		call    int
	}{
		{name: "pending selection", mapName: throtlPendingMap, call: 1},
		{name: "aggregate confirmation", mapName: throtlWaitAggregateMap, call: 2},
		{name: "pending confirmation", mapName: throtlPendingMap, call: 2},
	} {
		for _, failure := range []string{"rpc", "duplicate cursor", "incomplete traversal"} {
			t.Run(stage.name+"/"+failure, func(t *testing.T) {
				object, collector, active, retired := newThrotlRetirementTestState(t)
				object.maps[throtlPendingMap] = append(object.maps[throtlPendingMap],
					throtlObjectTestItem(throtlPendingKey{TD: active.td, Bio: 0x6000}, uint64(456)))
				object.dumpResult = func(name string, items []bpf.MapItem) ([]bpf.MapItem, error) {
					if name != stage.mapName || object.dumpCalls[name] != stage.call {
						return items, nil
					}
					if failure == "rpc" {
						return nil, errors.New("cleanup map read failed")
					}
					if failure == "incomplete traversal" {
						return nil, ebpf.ErrIterationAborted
					}
					// A concurrently removed cursor can restart a live map scan.
					return append(items, items[0]), nil
				}

				metrics, err := collector.Update()
				require.NoError(t, err)
				requireThrotlHostAndOtherMetrics(t, metrics, "8:1", "read", 3, 1)
				require.True(t, throtlTestTDExists(object.maps[throtlTDMap], retired.td))
				require.Nil(t, context.Cause(collector.session.breaker))
				require.Equal(t, uint64(8),
					collector.session.previous[throtlTestRawKey(active)].counters[0].DelayedCount)
				require.NotContains(t, collector.session.previous, throtlTestRawKey(retired))

				object.dumpResult = nil
				setThrotlTestAggregate(object, active, stableThrotlTestLane(10, 1_000))
				metrics, err = collector.Update()
				require.NoError(t, err)
				requireThrotlHostAndOtherMetrics(t, metrics, "8:1", "read", 2, 1)
				require.False(t, throtlTestTDExists(object.maps[throtlTDMap], retired.td))
			})
		}
	}
}

func TestThrotlCleanupMissingMapStopsSession(t *testing.T) {
	object, collector, _, retired := newThrotlRetirementTestState(t)
	delete(object.mapIDs, throtlPendingMap)

	metrics, err := collector.Update()
	require.ErrorIs(t, err, types.ErrTracingStopped)
	require.Empty(t, metrics)
	require.ErrorIs(t, context.Cause(collector.session.breaker), types.ErrTracingStopped)
	require.True(t, throtlTestTDExists(object.maps[throtlTDMap], retired.td))
}

func TestThrotlCleanupTraversalRequiresUniqueDump(t *testing.T) {
	full := make([]bpf.MapItem, throtlPendingEntries)
	for index := range full {
		full[index].Key = encodeThrotlTestDataValue(throtlPendingKey{
			TD:  1,
			Bio: uint64(index + 1),
		})
	}
	require.NoError(t, validateThrotlCleanupTraversal(
		throtlPendingMap,
		full,
	), "a complete map remains a valid cleanup input")

	full[len(full)-1].Key = append([]byte(nil), full[0].Key...)
	err := validateThrotlCleanupTraversal(
		throtlPendingMap,
		full,
	)
	require.ErrorIs(t, err, errThrotlSnapshotBusy)
}

func newThrotlRetirementTestState(t *testing.T) (
	*statefulThrotlCollectorBPF,
	*throtlTracing,
	throtlTestKey,
	throtlTestKey,
) {
	t.Helper()
	active := normalizedThrotlTestKey(throtlTestKey{
		td: 0x1000, blkg: 0x2000, css: 0xa1, serial: 11, operation: 0,
	})
	retired := normalizedThrotlTestKey(throtlTestKey{
		td: 0x3000, blkg: 0x4000, css: 0xb2, serial: 22, operation: 1,
	})
	object := newStatefulThrotlCollectorBPF()
	object.maps[throtlWaitAggregateMap] = []bpf.MapItem{
		throtlTestItem(active, stableThrotlTestLane(8, 800)),
		throtlTestItem(retired, stableThrotlTestLane(20, 4_000)),
	}
	object.maps[throtlBLKGOwnerMap] = []bpf.MapItem{
		throtlObjectTestItem(active.blkg, throtlTestBLKGOwner{
			TD: active.td, CSS: 0xa1, CSSSerial: active.serial,
		}),
		throtlObjectTestItem(retired.blkg, throtlTestBLKGOwner{
			TD: retired.td, CSS: 0xb2, CSSSerial: retired.serial,
		}),
	}
	object.maps[throtlPendingMap] = []bpf.MapItem{
		throtlObjectTestItem(
			throtlPendingKey{TD: retired.td, Bio: 0x5000},
			uint64(123),
		),
	}
	object.maps[throtlTDMap] = []bpf.MapItem{
		throtlObjectTestItem(active.td, throtlTDValue{
			State: throtlTDActive, Major: 8, Minor: 1,
		}),
		throtlObjectTestItem(retired.td, throtlTDValue{
			State: throtlTDRetired, Major: 8, Minor: 2,
		}),
	}
	collector := newThrotlTestCollector(t, object, 1)
	collector.session.previous[throtlTestRawKey(active)] = throtlWaitSample{
		device:    "8:1",
		operation: "read",
		counters: []throtlWaitCounters{{
			DelayedCount: 5,
			Wait10US:     500,
		}},
	}
	collector.session.previous[throtlTestRawKey(retired)] = throtlWaitSample{
		device:    "8:2",
		operation: "write",
		counters: []throtlWaitCounters{{
			DelayedCount: 10,
			Wait10US:     2_000,
		}},
	}
	return object, collector, active, retired
}

func throtlObjectTestItem(key, value any) bpf.MapItem {
	return bpf.MapItem{
		Key:   encodeThrotlTestDataValue(key),
		Value: encodeThrotlTestDataValue(value),
	}
}

func throtlTestTDExists(items []bpf.MapItem, want uint64) bool {
	for _, item := range items {
		var td uint64
		if err := decodeBPFMapData(item.Key, &td); err == nil && td == want {
			return true
		}
	}
	return false
}

func setThrotlTestAggregate(
	object *statefulThrotlCollectorBPF,
	key throtlTestKey,
	lane throtlTestLane,
) {
	want := throtlTestKeyBytes(key)
	items := object.maps[throtlWaitAggregateMap]
	for index := range items {
		if bytes.Equal(items[index].Key, want) {
			items[index].Value = throtlTestValueBytes(lane)
			object.maps[throtlWaitAggregateMap] = items
			return
		}
	}
	object.maps[throtlWaitAggregateMap] = append(
		items,
		throtlTestItem(key, lane),
	)
}
