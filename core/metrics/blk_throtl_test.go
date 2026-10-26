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

// Blk-throttle tests keep statistics, hook compatibility, and BPF lifecycle contracts together.
package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Statistics, CSS attribution, and interval baselines.

// Per-CPU snapshot and metric fixtures.

const blkThrotlBLKGOwnerMap = "blk_throtl_blkg_owner_map"

type blkThrotlTestDump struct {
	items []bpf.MapItem
	err   error
}

type fakeBlkThrotlCollectorBPF struct {
	bpf.BPF
	dumps           []blkThrotlTestDump
	retired         []bpf.MapItem
	retiredErr      error
	onRetiredDump   func()
	onAggregateDump func()
	deleteErr       map[string]error
	deleted         map[string]bool
	statusMapID     uint32
	aggregateMapID  uint32
	statusValue     []byte
	statusErr       error
	statusErrAtCall int
	deviceChanges   []uint64
	dumpCalls       int
	statusCalls     int
}

func newFakeBlkThrotlCollectorBPF() *fakeBlkThrotlCollectorBPF {
	return &fakeBlkThrotlCollectorBPF{
		statusMapID:    1,
		aggregateMapID: 2,
		statusValue:    make([]byte, binary.Size(ioControlStatus{})),
		deleteErr:      make(map[string]error),
		deleted:        make(map[string]bool),
	}
}

func (b *fakeBlkThrotlCollectorBPF) DumpMapByName(
	name string,
) ([]bpf.MapItem, error) {
	if name == blkThrotlRetiredMap {
		items := append([]bpf.MapItem(nil), b.retired...)
		if b.onRetiredDump != nil {
			b.onRetiredDump()
		}
		return items, b.retiredErr
	}
	if name != blkThrotlWaitAggregateMap {
		return nil, fmt.Errorf("unexpected map %q", name)
	}
	b.dumpCalls++
	if len(b.dumps) == 0 {
		return nil, fmt.Errorf("unexpected extra %s dump", name)
	}
	result := b.dumps[0]
	b.dumps = b.dumps[1:]
	if b.onAggregateDump != nil {
		b.onAggregateDump()
	}
	return result.items, result.err
}

func (b *fakeBlkThrotlCollectorBPF) DeleteMapItems(mapID uint32, keys [][]byte) error {
	for _, key := range keys {
		identity := fmt.Sprintf("%d:%x", mapID, key)
		if err := b.deleteErr[identity]; err != nil {
			return err
		}
		b.deleted[identity] = true
		if mapID == b.MapIDByName(blkThrotlRetiredMap) {
			for index, item := range b.retired {
				if bytes.Equal(item.Key, key) {
					b.retired = append(b.retired[:index], b.retired[index+1:]...)
					break
				}
			}
		}
	}
	return nil
}

func (b *fakeBlkThrotlCollectorBPF) dumpBatch(
	mapID uint32,
) ([]bpf.MapItem, error) {
	if mapID != b.MapIDByName(blkThrotlWaitAggregateMap) {
		return nil, fmt.Errorf("unexpected batch map ID %d", mapID)
	}
	return b.DumpMapByName(blkThrotlWaitAggregateMap)
}

func (b *fakeBlkThrotlCollectorBPF) MapIDByName(name string) uint32 {
	switch name {
	case ioControlStatusMap:
		return b.statusMapID
	case blkThrotlWaitAggregateMap:
		return b.aggregateMapID
	case blkThrotlRetiredMap:
		return 3
	default:
		return 0
	}
}

func (b *fakeBlkThrotlCollectorBPF) ReadMap(
	mapID uint32,
	key []byte,
) ([]byte, error) {
	b.statusCalls++
	if mapID != b.statusMapID {
		return nil, fmt.Errorf("unexpected status map ID %d", mapID)
	}
	if len(key) != 4 || binary.LittleEndian.Uint32(key) != 0 {
		return nil, fmt.Errorf("unexpected status key %v", key)
	}
	if b.statusErr != nil && (b.statusErrAtCall == 0 || b.statusCalls == b.statusErrAtCall) {
		return nil, b.statusErr
	}
	return blkThrotlTestStatusValue(b.statusValue, &b.deviceChanges), nil
}

func nextBlkThrotlTestDeviceChange(values *[]uint64) uint64 {
	if len(*values) == 0 {
		return 0
	}
	value := (*values)[0]
	if len(*values) > 1 {
		*values = (*values)[1:]
	}
	return value
}

func (b *fakeBlkThrotlCollectorBPF) setDumps(results ...blkThrotlTestDump) {
	b.dumps = append([]blkThrotlTestDump(nil), results...)
}

type reusedBlkThrotlCollectorBPF struct {
	*fakeBlkThrotlCollectorBPF
	batchDumps []blkThrotlTestDump
}

func (b *reusedBlkThrotlCollectorBPF) dumpBatch(
	mapID uint32,
) ([]bpf.MapItem, error) {
	if mapID != b.MapIDByName(blkThrotlWaitAggregateMap) {
		return nil, fmt.Errorf("unexpected batch map ID %d", mapID)
	}
	if len(b.batchDumps) == 0 {
		return nil, fmt.Errorf("unexpected extra %s batch dump", blkThrotlWaitAggregateMap)
	}
	result := b.batchDumps[0]
	b.batchDumps = b.batchDumps[1:]
	return result.items, result.err
}

type blkThrotlTestKey struct {
	serial    uint64
	css       uint64
	major     uint32
	minor     uint32
	operation uint32
}

type blkThrotlTestLane = ioControlWaitCounters

type blkThrotlTestBLKGOwner struct {
	CSS       uint64
	CSSSerial uint64
	Major     uint32
	Minor     uint32
}

func stableBlkThrotlTestLane(count, wait10US uint64) blkThrotlTestLane {
	return blkThrotlTestLane{
		IOCount:  count,
		Wait10US: wait10US,
	}
}

func blkThrotlTestRawKey(key blkThrotlTestKey) ioControlWaitKey {
	if key.serial == 0 {
		key.serial = key.css
	}
	if key.css == 0 {
		key.css = key.serial
	}
	return ioControlWaitKey{
		CSS: key.css, CSSSerial: key.serial,
		Major: key.major, Minor: key.minor, Operation: key.operation,
	}
}

func blkThrotlTestItem(
	key blkThrotlTestKey,
	lanes ...blkThrotlTestLane,
) bpf.MapItem {
	return ioControlWaitTestItem(blkThrotlTestRawKey(key), lanes...)
}

func newBlkThrotlTestCollector(
	t *testing.T,
	object ioControlTestBPF,
	possibleCPUs int,
) *blkThrotlTracing {
	t.Helper()
	ioControlTestDiskstats(t)
	breaker, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() { cancel(nil) })
	return &blkThrotlTracing{session: &blkThrotlSession{
		object:          object,
		hashReader:      ioControlHashReader{lookupBatch: object.dumpBatch},
		possibleCPUs:    possibleCPUs,
		previous:        make(ioControlWaitSnapshot),
		breaker:         breaker,
		cancel:          cancel,
		containerSource: emptyBlkThrotlTestContainerSource,
	}}
}

func emptyBlkThrotlTestContainerSource() (map[string]*pod.Container, error) {
	return nil, nil
}

func blkThrotlMetricByNameAndScope(
	t *testing.T,
	metrics []*metric.Data,
	wantName string,
	wantScope string,
) *metric.Data {
	t.Helper()
	var matches []*metric.Data
	for _, data := range metrics {
		if data.Name() == wantName && data.Labels()["scope"] == wantScope {
			matches = append(matches, data)
		}
	}
	require.Len(t, matches, 1, "metric %s scope %s", wantName, wantScope)
	return matches[0]
}

func blkThrotlCustomLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		if key == metric.LabelHost || key == metric.LabelRegion {
			continue
		}
		result[key] = value
	}
	return result
}

func requireBlkThrotlHostAndOtherMetrics(
	t *testing.T,
	metrics []*metric.Data,
	device string,
	operation string,
	wantCount float64,
	wantAverageMS float64,
) {
	t.Helper()
	require.Len(t, metrics, 4)
	for _, scope := range []string{ioControlHostScope, ioControlOtherScope} {
		wantLabels := map[string]string{
			"disk":      device,
			"operation": operation,
			"scope":     scope,
		}

		count := blkThrotlMetricByNameAndScope(t, metrics,
			"delayed_io_count", scope)
		require.Equal(t, metric.MetricTypeGauge, count.Type())
		require.Equal(t, wantCount, count.Value)
		require.Equal(t, wantLabels, blkThrotlCustomLabels(count.Labels()))
		require.NotEmpty(t, count.Help())
		require.Contains(t, strings.ToLower(count.Help()), "blk-throttle")

		average := blkThrotlMetricByNameAndScope(t, metrics,
			"average_wait_ms", scope)
		require.Equal(t, metric.MetricTypeGauge, average.Type())
		require.Equal(t, wantAverageMS, average.Value)
		require.Equal(t, wantLabels, blkThrotlCustomLabels(average.Labels()))
		require.NotEmpty(t, average.Help())
		require.Contains(t, strings.ToLower(average.Help()), "blk-throttle")
	}
}

// Strict map decoding and raw collection boundaries.

func TestBlkThrotlSnapshotRetriesCounterTraversal(t *testing.T) {
	key := blkThrotlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	baseline := blkThrotlTestItem(key, stableBlkThrotlTestLane(10, 1_000))
	current := blkThrotlTestItem(key, stableBlkThrotlTestLane(15, 2_000))
	object := newFakeBlkThrotlCollectorBPF()
	collector := newBlkThrotlTestCollector(t, object, 1)
	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{baseline}})
	_, err := collector.Update()
	require.NoError(t, err)

	object.setDumps(
		blkThrotlTestDump{items: []bpf.MapItem{
			blkThrotlTestItem(key, stableBlkThrotlTestLane(1_000, 100_000)),
			blkThrotlTestItem(key, stableBlkThrotlTestLane(1_001, 100_100)),
		}},
		blkThrotlTestDump{items: []bpf.MapItem{current}},
	)
	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Nil(t, context.Cause(collector.session.breaker))
	requireBlkThrotlHostAndOtherMetrics(t, metrics, "sda", "read", 5, 2)
}

// Interval baselines, retries, health stops, and per-CPU wrap.

func TestBlkThrotlIntervalStartsFromEmptyMapAndRestarts(t *testing.T) {
	var unstarted blkThrotlTracing
	metrics, err := unstarted.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Nil(t, metrics)

	key := blkThrotlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	object := newFakeBlkThrotlCollectorBPF()
	collector := newBlkThrotlTestCollector(t, object, 1)

	// Every BPF object owns a freshly-created empty aggregate map. Completed
	// episodes before its first scrape therefore have a known zero predecessor.
	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(key, stableBlkThrotlTestLane(3, 900)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndOtherMetrics(t, metrics,
		"sda", "read", 3, 3)

	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(key, stableBlkThrotlTestLane(5, 1_500)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndOtherMetrics(t, metrics,
		"sda", "read", 2, 3)

	// A replacement object starts another known-zero counter generation.
	replacementObject := newFakeBlkThrotlCollectorBPF()
	replacement := newBlkThrotlTestCollector(t, replacementObject, 1).session
	collector.session = replacement
	replacementObject.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(key, stableBlkThrotlTestLane(100, 30_000)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndOtherMetrics(t, metrics,
		"sda", "read", 100, 3)

	replacementObject.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(key, stableBlkThrotlTestLane(102, 30_600)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndOtherMetrics(t, metrics,
		"sda", "read", 2, 3)

	collector.session = nil
	metrics, err = collector.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Empty(t, metrics)
}

func TestBlkThrotlRetryableIntervalFailuresRebuildBaseline(t *testing.T) {
	key := blkThrotlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	baseline := blkThrotlTestItem(key,
		stableBlkThrotlTestLane(10, 1_000))
	current := blkThrotlTestItem(key,
		stableBlkThrotlTestLane(15, 2_000))
	next := blkThrotlTestItem(key,
		stableBlkThrotlTestLane(17, 2_800))

	for _, test := range []struct {
		name      string
		initial   bool
		attempts  int
		configure func(*fakeBlkThrotlCollectorBPF)
		failure   blkThrotlTestDump
	}{
		{
			name:     "initial aggregate dump",
			initial:  true,
			attempts: 2,
			failure:  blkThrotlTestDump{err: errors.New("aggregate dump failed")},
		},
		{
			name:     "aggregate dump",
			attempts: 2,
			failure:  blkThrotlTestDump{err: errors.New("aggregate dump failed")},
		},
		{
			name:     "aggregate traversal",
			attempts: 2,
			failure:  blkThrotlTestDump{items: []bpf.MapItem{current, current}},
		},
		{
			name:     "wait without delayed IO",
			attempts: 1,
			failure: blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(10, 1_100)),
			}},
		},
		{
			name:     "health status read",
			attempts: 2,
			configure: func(object *fakeBlkThrotlCollectorBPF) {
				object.statusErr = errors.New("status read failed")
				object.statusErrAtCall = object.statusCalls + 2
			},
			failure: blkThrotlTestDump{items: []bpf.MapItem{current}},
		},
		{
			name:     "entry status read",
			attempts: 2,
			configure: func(object *fakeBlkThrotlCollectorBPF) {
				object.statusErr = errors.New("device change read failed")
			},
			failure: blkThrotlTestDump{items: []bpf.MapItem{current}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlTestCollector(t, object, 1)
			if !test.initial {
				object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{baseline}})
				_, err := collector.Update()
				require.NoError(t, err)
			}

			for range test.attempts {
				if test.configure != nil {
					test.configure(object)
				}
				object.setDumps(test.failure, test.failure, test.failure)
				metrics, err := collector.Update()
				require.Error(t, err)
				if test.failure.err != nil {
					require.ErrorIs(t, err, test.failure.err)
				}
				require.Empty(t, metrics)
				require.Nil(t, context.Cause(collector.session.breaker))
				require.Nil(t, collector.session.previous)
			}

			object.statusMapID = 1
			object.statusErr = nil
			object.statusErrAtCall = 0
			object.statusValue = make([]byte, binary.Size(ioControlStatus{}))
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{current}})
			metrics, err := collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Empty(t, metrics)
			require.Nil(t, context.Cause(collector.session.breaker))

			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{next}})
			metrics, err = collector.Update()
			require.NoError(t, err)
			requireBlkThrotlHostAndOtherMetrics(t, metrics,
				"sda", "read", 2, 4)
		})
	}
}

func TestBlkThrotlDeviceChangeDiscardsInterval(t *testing.T) {
	key := blkThrotlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	baseline := blkThrotlTestItem(key, stableBlkThrotlTestLane(100, 1_000))
	for _, test := range []struct {
		name        string
		changes     []uint64
		count       uint64
		hasCaptured bool
		configure   func(*fakeBlkThrotlCollectorBPF)
	}{
		{name: "before snapshot", changes: []uint64{1}},
		{name: "during snapshot with wrapped count", changes: []uint64{0, 1}, count: 1, hasCaptured: true},
		{name: "during snapshot with unchanged count", changes: []uint64{0, 1}, count: 100, hasCaptured: true},
		{
			name: "BPF error during transition", changes: []uint64{0, 1}, count: 105, hasCaptured: true,
			configure: func(object *fakeBlkThrotlCollectorBPF) {
				object.statusValue = ioControlTestBytes(ioControlTestStatus(blkThrotlFailureOwner, 0))
			},
		},
		{
			name: "invalid map during transition", changes: []uint64{0, 1},
			configure: func(object *fakeBlkThrotlCollectorBPF) {
				object.aggregateMapID = 0
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlTestCollector(t, object, 1)
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{baseline}})
			_, err := collector.Update()
			require.NoError(t, err)
			previous := collector.session.previous

			object.deviceChanges = test.changes
			if test.configure != nil {
				test.configure(object)
			}
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(test.count, 10)),
			}})
			metrics, err := collector.Update()
			require.ErrorIs(t, err, errIOControlDevicesChanged)
			require.NotErrorIs(t, err, types.ErrTracingStopped)
			if test.hasCaptured {
				requireBlkThrotlHostAndOtherMetrics(t, metrics, "sda", "read", 0, 0)
			} else {
				require.Nil(t, metrics)
			}
			require.ErrorIs(t, context.Cause(collector.session.breaker), errIOControlDevicesChanged)
			require.Equal(t, previous, collector.session.previous)

			metrics, err = collector.Update()
			require.ErrorIs(t, err, errIOControlDevicesChanged)
			require.Nil(t, metrics)
			require.Equal(t, previous, collector.session.previous)
		})
	}
}

func TestBlkThrotlDeviceReadFailureDefersTerminalStop(t *testing.T) {
	object := newFakeBlkThrotlCollectorBPF()
	object.statusValue = ioControlTestBytes(ioControlTestStatus(blkThrotlFailureOwner, 0))
	object.statusErr = unix.EIO
	object.statusErrAtCall = 3
	collector := newBlkThrotlTestCollector(t, object, 1)
	object.setDumps(blkThrotlTestDump{})

	metrics, err := collector.Update()
	require.ErrorIs(t, err, unix.EIO)
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	require.Empty(t, metrics)
	require.Nil(t, context.Cause(collector.session.breaker))
	require.Empty(t, collector.session.previous)

	object.statusErr = nil
	object.setDumps(blkThrotlTestDump{})
	metrics, err = collector.Update()
	require.ErrorIs(t, err, types.ErrTracingStopped)
	require.Empty(t, metrics)
	require.ErrorIs(t, context.Cause(collector.session.breaker), types.ErrTracingStopped)
}

func TestBlkThrotlBatchErrorsDoNotFallbackOrAdvanceBaseline(t *testing.T) {
	key := blkThrotlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	baseline := blkThrotlTestItem(key, stableBlkThrotlTestLane(10, 1_000))
	current := blkThrotlTestItem(key, stableBlkThrotlTestLane(15, 2_000))
	next := blkThrotlTestItem(key, stableBlkThrotlTestLane(17, 2_800))
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
					blkThrotlTestItem(key, stableBlkThrotlTestLane(0, 0)),
				},
			},
		} {
			t.Run(failure.name+"/"+partial.name, func(t *testing.T) {
				object := &reusedBlkThrotlCollectorBPF{
					fakeBlkThrotlCollectorBPF: newFakeBlkThrotlCollectorBPF(),
					batchDumps: []blkThrotlTestDump{
						{items: []bpf.MapItem{baseline}},
						{items: partial.items, err: failure.err},
						{items: []bpf.MapItem{current}},
						{items: []bpf.MapItem{next}},
					},
				}
				collector := newBlkThrotlTestCollector(t, object, 1)
				_, err := collector.Update()
				require.NoError(t, err)

				// An available single-key dump must not hide an unrelated
				// batch failure or commit the partial rows as a baseline.
				object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{current}})
				metrics, err := collector.Update()
				require.ErrorIs(t, err, failure.err)
				require.Empty(t, metrics)
				require.Zero(t, object.dumpCalls)
				require.Nil(t, context.Cause(collector.session.breaker))
				require.Nil(t, collector.session.previous)

				metrics, err = collector.Update()
				require.ErrorIs(t, err, metric.ErrNoData)
				require.Empty(t, metrics)
				require.Zero(t, object.dumpCalls)

				metrics, err = collector.Update()
				require.NoError(t, err)
				requireBlkThrotlHostAndOtherMetrics(t, metrics,
					"sda", "read", 2, 4)
				require.Zero(t, object.dumpCalls)
			})
		}
	}
}

func TestBlkThrotlTerminalHealthFailuresStopSession(t *testing.T) {
	key := blkThrotlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	current := blkThrotlTestItem(key,
		stableBlkThrotlTestLane(15, 2_000))

	for _, test := range []struct {
		name      string
		configure func(*fakeBlkThrotlCollectorBPF)
	}{
		{
			name: "missing status map",
			configure: func(object *fakeBlkThrotlCollectorBPF) {
				object.statusMapID = 0
			},
		},
		{
			name: "short status",
			configure: func(object *fakeBlkThrotlCollectorBPF) {
				object.statusValue = make(
					[]byte, binary.Size(ioControlStatus{})-1,
				)
			},
		},
		{
			name: "long status",
			configure: func(object *fakeBlkThrotlCollectorBPF) {
				object.statusValue = make(
					[]byte, binary.Size(ioControlStatus{})+1,
				)
			},
		},
		{
			name: "unhealthy status",
			configure: func(object *fakeBlkThrotlCollectorBPF) {
				object.statusValue = ioControlTestBytes(ioControlTestStatus(blkThrotlFailureOwner, 0))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlTestCollector(t, object, 1)
			test.configure(object)
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{current}})

			_, err := collector.Update()
			require.Error(t, err)
			require.ErrorIs(t, err, types.ErrTracingStopped)
			require.ErrorIs(t, context.Cause(collector.session.breaker), types.ErrTracingStopped)
		})
	}
}

func TestBlkThrotlSnapshotDoesNotReadReusedAggregateValue(t *testing.T) {
	key := blkThrotlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
	baseline := blkThrotlTestItem(key, stableBlkThrotlTestLane(100, 10_000))
	current := blkThrotlTestItem(key, stableBlkThrotlTestLane(101, 10_200))
	object := &reusedBlkThrotlCollectorBPF{
		fakeBlkThrotlCollectorBPF: newFakeBlkThrotlCollectorBPF(),
		batchDumps: []blkThrotlTestDump{
			{items: []bpf.MapItem{baseline}},
			{items: []bpf.MapItem{current}},
		},
	}
	// A single-key lookup can retain the old key while its map storage is
	// deleted and reused for a new counter. Batch copying keeps them paired.
	object.setDumps(
		blkThrotlTestDump{items: []bpf.MapItem{baseline}},
		blkThrotlTestDump{items: []bpf.MapItem{
			blkThrotlTestItem(key, stableBlkThrotlTestLane(0, 0)),
		}},
	)
	collector := newBlkThrotlTestCollector(t, object, 1)
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndOtherMetrics(t, metrics,
		"sda", "read", 100, 1)

	metrics, err = collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndOtherMetrics(t, metrics,
		"sda", "read", 1, 2)
}

// Host metrics preserve weighted aggregates and dump-order independence.

func TestBlkThrotlAggregateMetricsIgnoreDumpOrder(t *testing.T) {
	keys := []blkThrotlTestKey{
		{css: 3, major: 4095, minor: 1, operation: 1},
		{css: 2, major: 4094, minor: 2, operation: 0},
		{css: 1, major: 4095, minor: 1, operation: 0},
	}
	object := newFakeBlkThrotlCollectorBPF()
	collector := newBlkThrotlTestCollector(t, object, 1)
	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(keys[0], stableBlkThrotlTestLane(10, 1_000)),
		blkThrotlTestItem(keys[1], stableBlkThrotlTestLane(20, 2_000)),
		blkThrotlTestItem(keys[2], stableBlkThrotlTestLane(30, 3_000)),
	}})
	_, err := collector.Update()
	require.NoError(t, err)

	// Reversing raw rows must preserve every series and its interval values.
	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(keys[2], stableBlkThrotlTestLane(31, 3_200)),
		blkThrotlTestItem(keys[1], stableBlkThrotlTestLane(21, 2_200)),
		blkThrotlTestItem(keys[0], stableBlkThrotlTestLane(11, 1_200)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 12)

	var signatures []string
	wantValues := map[string]float64{
		blkThrotlDelayedCountName: 1,
		blkThrotlAverageWaitName:  2,
	}
	for _, data := range metrics {
		got := data
		require.Equal(t, wantValues[got.Name()], got.Value)
		labels := blkThrotlCustomLabels(got.Labels())
		signatures = append(signatures, fmt.Sprintf("%s|%s|%s|%s",
			labels["disk"], labels["operation"], got.Name(),
			labels["scope"]))
	}
	require.ElementsMatch(t, []string{
		"nvme0n1|read|delayed_io_count|host",
		"nvme0n1|read|average_wait_ms|host",
		"sda|read|delayed_io_count|host",
		"sda|read|average_wait_ms|host",
		"sda|write|delayed_io_count|host",
		"sda|write|average_wait_ms|host",
		"nvme0n1|read|delayed_io_count|other",
		"nvme0n1|read|average_wait_ms|other",
		"sda|read|delayed_io_count|other",
		"sda|read|average_wait_ms|other",
		"sda|write|delayed_io_count|other",
		"sda|write|average_wait_ms|other",
	}, signatures)
}

// CSS attribution and serial-aware baseline fixtures.

func newBlkThrotlAttributedTestCollector(
	t *testing.T,
	object *fakeBlkThrotlCollectorBPF,
	source *ioControlAttributionTestSource,
) *blkThrotlTracing {
	t.Helper()
	collector := newBlkThrotlTestCollector(t, object, 1)
	collector.session.containerSource = source.read
	return collector
}

func blkThrotlAttributionMetricValues(
	t *testing.T,
	metrics []*metric.Data,
) map[string]float64 {
	t.Helper()
	values := make(map[string]float64, len(metrics))
	for _, data := range metrics {
		got := data
		labels := blkThrotlCustomLabels(got.Labels())
		kind := labels["scope"]
		if strings.HasPrefix(got.Name(), "container_") {
			kind = "container"
		}
		values[kind+"/"+got.Name()] = got.Value
	}
	return values
}

func requireBlkThrotlHostAndOtherInterval(
	t *testing.T,
	metrics []*metric.Data,
	count float64,
	averageMS float64,
) {
	t.Helper()
	require.Equal(t, map[string]float64{
		"host/delayed_io_count":  count,
		"host/average_wait_ms":   averageMS,
		"other/delayed_io_count": count,
		"other/average_wait_ms":  averageMS,
	}, blkThrotlAttributionMetricValues(t, metrics))
}

func requireBlkThrotlHostAndContainerInterval(
	t *testing.T,
	metrics []*metric.Data,
	count float64,
	averageMS float64,
) {
	t.Helper()
	require.Equal(t, map[string]float64{
		"host/delayed_io_count":                count,
		"host/average_wait_ms":                 averageMS,
		"container/container_delayed_io_count": count,
		"container/container_average_wait_ms":  averageMS,
	}, blkThrotlAttributionMetricValues(t, metrics))
}

// Public Other/container accounting and equal-label aggregation.

func TestBlkThrotlRemovedLateCompletionNeverDeletesActiveAggregate(
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
	object := newFakeBlkThrotlCollectorBPF()
	collector := newBlkThrotlAttributedTestCollector(t, object, source)
	key := blkThrotlTestKey{
		css: 0x11, major: 4095, minor: 1, operation: 0,
	}
	waitKey := blkThrotlTestRawKey(key)

	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(key, stableBlkThrotlTestLane(10, 1_000)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndContainerInterval(t, metrics, 10, 1)

	// Once the shared query stops returning the container, its completions go
	// to Other. The raw aggregate remains live while old bios can complete.
	source.containers = nil
	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(key, stableBlkThrotlTestLane(11, 1_100)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndOtherInterval(t, metrics, 1, 1)

	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(key, stableBlkThrotlTestLane(12, 1_400)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndOtherInterval(t, metrics, 1, 3)

	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(key, stableBlkThrotlTestLane(13, 1_900)),
	}})
	metrics, err = collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndOtherInterval(t, metrics, 1, 5)
	_, retained := collector.session.previous[waitKey]
	require.True(t, retained, "a live raw series must remain in the baseline")
}

// CSS serials distinguish raw counter lifetimes across attribution changes.

// Reusing the CSS address changes only the serial. The new
// cumulative row starts at zero even when the public CSS mapping is unchanged.
func TestBlkThrotlCSSSerialKeepsCounterBaselinesSeparate(t *testing.T) {
	for _, mapped := range []bool{true, false} {
		t.Run(map[bool]string{true: "container", false: "other"}[mapped], func(t *testing.T) {
			source := &ioControlAttributionTestSource{}
			if mapped {
				container := newIOControlAttributionTestContainer(
					"container", 0x11, time.Unix(100, 0),
				)
				source.containers = map[string]*pod.Container{container.ID: container}
			}
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlAttributedTestCollector(t, object, source)
			key := blkThrotlTestKey{
				css: 0x11, serial: 7, major: 4095, minor: 1,
			}
			requireInterval := requireBlkThrotlHostAndOtherInterval
			if mapped {
				requireInterval = requireBlkThrotlHostAndContainerInterval
			}
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(10, 1_000)),
			}})
			metrics, err := collector.Update()
			require.NoError(t, err)
			requireInterval(t, metrics, 10, 1)
			oldKey := blkThrotlTestRawKey(key)

			key.serial++
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(1, 200)),
			}})
			metrics, err = collector.Update()
			require.NoError(t, err)
			requireInterval(t, metrics, 1, 2)
			require.NotContains(t, collector.session.previous, oldKey)
			require.Contains(t, collector.session.previous, blkThrotlTestRawKey(key))

			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(2, 500)),
			}})
			metrics, err = collector.Update()
			require.NoError(t, err)
			requireInterval(t, metrics, 1, 3)
		})
	}
}

func blkThrotlRetiredTestItem(key blkThrotlTestKey) bpf.MapItem {
	raw := blkThrotlTestRawKey(key)
	identity := ioControlRetiredKey{
		CSS: raw.CSS, CSSSerial: raw.CSSSerial, Major: raw.Major, Minor: raw.Minor,
	}
	return bpf.MapItem{Key: ioControlTestBytes(identity), Value: []byte{1}}
}

func TestBlkThrotlRetiredTailIsConsumedOnce(t *testing.T) {
	for _, hasBaseline := range []bool{false, true} {
		t.Run(fmt.Sprintf("previous scrape=%t", hasBaseline), func(t *testing.T) {
			container := newIOControlAttributionTestContainer("old-container", 0x11, time.Unix(100, 0))
			source := &ioControlAttributionTestSource{containers: map[string]*pod.Container{container.ID: container}}
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlAttributedTestCollector(t, object, source)
			key := blkThrotlTestKey{css: 0x11, serial: 111, major: 4095, minor: 1}
			if hasBaseline {
				object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
					blkThrotlTestItem(key, stableBlkThrotlTestLane(10, 1_000)),
				}})
				_, err := collector.Update()
				require.NoError(t, err)
			}
			count, wait := uint64(3), uint64(900)
			if hasBaseline {
				count, wait = 13, 1_900
			}
			object.retired = []bpf.MapItem{blkThrotlRetiredTestItem(key)}
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(count, wait)),
			}})
			metrics, err := collector.Update()
			require.NoError(t, err)
			requireBlkThrotlHostAndOtherInterval(
				t,
				metrics,
				3,
				3,
			)
			require.Empty(t, collector.session.previous)
			require.Empty(t, object.retired)

			object.setDumps(blkThrotlTestDump{})
			metrics, err = collector.Update()
			require.NoError(t, err)
			require.Empty(t, metrics, "retired data must not be published twice")
		})
	}
}

func TestBlkThrotlRetirementDuringScrapeWaitsForNextSnapshot(t *testing.T) {
	for _, moment := range []string{"after retired read", "during counter read", "after commit"} {
		t.Run(moment, func(t *testing.T) {
			container := newIOControlAttributionTestContainer("container", 0x11, time.Unix(100, 0))
			source := &ioControlAttributionTestSource{containers: map[string]*pod.Container{container.ID: container}}
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlAttributedTestCollector(t, object, source)
			key := blkThrotlTestKey{css: 0x11, serial: 111, major: 4095, minor: 1}
			retire := func() { object.retired = []bpf.MapItem{blkThrotlRetiredTestItem(key)} }
			switch moment {
			case "after retired read":
				object.onRetiredDump = retire
			case "during counter read":
				object.onAggregateDump = retire
			}
			firstCount, firstWait := uint64(1), uint64(100)
			if moment == "after retired read" {
				firstCount, firstWait = 2, 400
			}
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(firstCount, firstWait)),
			}})
			metrics, err := collector.Update()
			require.NoError(t, err)
			requireBlkThrotlHostAndContainerInterval(
				t,
				metrics,
				float64(firstCount),
				float64(firstWait)/100/float64(firstCount),
			)
			require.Contains(t, collector.session.previous, blkThrotlTestRawKey(key))
			require.Empty(t, object.deleted, "late retirement must not reclaim the earlier snapshot")
			if moment == "after commit" {
				retire()
			}
			object.onRetiredDump = nil
			object.onAggregateDump = nil
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(2, 400)),
			}})
			metrics, err = collector.Update()
			require.NoError(t, err)
			if moment == "after retired read" {
				requireBlkThrotlHostAndOtherInterval(
					t,
					metrics,
					0,
					0,
				)
			} else {
				requireBlkThrotlHostAndOtherInterval(
					t,
					metrics,
					1,
					3,
				)
			}
			require.Empty(t, collector.session.previous)
			require.Empty(t, object.retired)
		})
	}
}

func TestBlkThrotlRetirementReclaimFailurePreservesConsumedBaseline(t *testing.T) {
	for _, failure := range []string{"read counter", "write counter", "retired marker"} {
		t.Run(failure, func(t *testing.T) {
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlTestCollector(t, object, 1)
			key := blkThrotlTestKey{css: 0x11, serial: 111, major: 4095, minor: 1}
			readKey := blkThrotlTestRawKey(key)
			writeKey := readKey
			writeKey.Operation = 1
			retired := blkThrotlRetiredTestItem(key)
			object.retired = []bpf.MapItem{retired}
			failedID, failedKey := object.aggregateMapID, ioControlTestBytes(readKey)
			switch failure {
			case "write counter":
				failedKey = ioControlTestBytes(writeKey)
			case "retired marker":
				failedID, failedKey = object.MapIDByName(blkThrotlRetiredMap), retired.Key
			}
			identity := fmt.Sprintf("%d:%x", failedID, failedKey)
			object.deleteErr[identity] = unix.EIO
			items := []bpf.MapItem{
				ioControlWaitTestItem(readKey, ioControlWaitCounters{IOCount: 2, Wait10US: 200}),
				ioControlWaitTestItem(writeKey, ioControlWaitCounters{IOCount: 3, Wait10US: 600}),
			}
			object.setDumps(blkThrotlTestDump{items: items})
			metrics, err := collector.Update()
			require.NoError(t, err, "reclaim failure is not collection failure")
			require.Len(t, metrics, 8)
			require.Nil(t, context.Cause(collector.session.breaker))
			require.NotNil(t, collector.session.previous)
			require.Len(t, object.retired, 1)
			for rawKey, lanes := range map[ioControlWaitKey][]ioControlWaitCounters{
				readKey: {{IOCount: 2, Wait10US: 200}}, writeKey: {{IOCount: 3, Wait10US: 600}},
			} {
				if !object.deleted[fmt.Sprintf("%d:%x", object.aggregateMapID, ioControlTestBytes(rawKey))] {
					require.Equal(t, lanes, collector.session.previous[rawKey])
				}
			}

			remaining := []bpf.MapItem{}
			for _, item := range items {
				if !object.deleted[fmt.Sprintf("%d:%x", object.aggregateMapID, item.Key)] {
					remaining = append(remaining, item)
				}
			}
			delete(object.deleteErr, identity)
			object.setDumps(blkThrotlTestDump{items: remaining})
			metrics, err = collector.Update()
			require.NoError(t, err)
			for _, data := range metrics {
				require.Zero(t, data.Value, "already consumed tail must not be emitted on reclaim retry")
			}
			require.Empty(t, object.retired)
			require.Empty(t, collector.session.previous)
		})
	}
}

func TestBlkThrotlRetiredCaptureFailureDoesNotDeleteCounters(t *testing.T) {
	for _, failure := range []string{"retired read", "counter read", "post-capture health"} {
		t.Run(failure, func(t *testing.T) {
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlTestCollector(t, object, 1)
			key := blkThrotlTestKey{css: 0x11, serial: 111, major: 4095, minor: 1}
			object.retired = []bpf.MapItem{blkThrotlRetiredTestItem(key)}
			current := blkThrotlTestItem(key, stableBlkThrotlTestLane(3, 900))
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{current}})
			switch failure {
			case "retired read":
				object.retiredErr = unix.EIO
			case "counter read":
				object.setDumps(blkThrotlTestDump{err: unix.EIO})
			case "post-capture health":
				object.statusErr = unix.EIO
				object.statusErrAtCall = 2
			}
			metrics, err := collector.Update()
			require.ErrorIs(t, err, unix.EIO)
			require.Empty(t, metrics)
			require.Empty(t, object.deleted)
			require.Nil(t, collector.session.previous)

			object.retiredErr = nil
			object.statusErr = nil
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{current}})
			metrics, err = collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Empty(t, metrics, "recovery establishes a baseline without failed-window catch-up")
			object.setDumps(blkThrotlTestDump{})
			metrics, err = collector.Update()
			require.NoError(t, err)
			require.Empty(t, metrics)
		})
	}
}

// Container failures preserve Host while raw counter baselines advance.

func TestBlkThrotlContainerFailuresPreserveHostIntervals(t *testing.T) {
	for _, failure := range []string{"query", "labels", "empty"} {
		t.Run(failure, func(t *testing.T) {
			container := newIOControlAttributionTestContainer("container", 0x11, time.Unix(100, 0))
			source := &ioControlAttributionTestSource{
				containers: map[string]*pod.Container{container.ID: container},
			}
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlAttributedTestCollector(t, object, source)
			key := blkThrotlTestKey{css: 0x11, major: 4095, minor: 1}
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
				object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
					blkThrotlTestItem(key, stableBlkThrotlTestLane(count, count*300)),
				}})
				metrics, err := collector.Update()
				require.NoError(t, err)
				requireBlkThrotlHostAndOtherInterval(t, metrics, 3, 3)
			}

			container.Labels[ioControlHostNamespaceKey] = "shared-namespace"
			source.err = nil
			source.containers = map[string]*pod.Container{container.ID: container}
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(8, 2_600)),
			}})
			metrics, err := collector.Update()
			require.NoError(t, err)
			requireBlkThrotlHostAndContainerInterval(t, metrics, 2, 4)
		})
	}
}

// Compatibility and lifecycle: hooks, BPF ownership, and session restarts.

// BPF ownership and startup fixtures.

const (
	blkThrotlObjectEntries  = 4096
	blkThrotlPendingEntries = 10240
)

type fakeBlkThrotlBPF struct {
	bpf.BPF
	options       []bpf.AttachOption
	attachErr     error
	onAttach      func()
	closeErr      error
	mapID         uint32
	value         []byte
	readErr       error
	deviceChanges []uint64
	closeCalls    int
}

func (b *fakeBlkThrotlBPF) AttachWithOptions(options []bpf.AttachOption) error {
	b.options = append(b.options, options...)
	if b.onAttach != nil {
		b.onAttach()
	}
	return b.attachErr
}

func (b *fakeBlkThrotlBPF) MapIDByName(name string) uint32 {
	switch name {
	case ioControlStatusMap:
		return b.mapID
	case blkThrotlWaitAggregateMap:
		return 2
	case blkThrotlPendingMap:
		return 4
	case blkThrotlRetiredMap:
		return 3
	default:
		return 0
	}
}

func (b *fakeBlkThrotlBPF) ReadMap(_ uint32, _ []byte) ([]byte, error) {
	return blkThrotlTestStatusValue(b.value, &b.deviceChanges), b.readErr
}

func testBlkThrotlHooks() *blkThrotlHooks {
	return &blkThrotlHooks{
		trackedRange: symbol.KsymbolRange{Start: 0x1000, End: 0x1100},
		ignoredRange: symbol.KsymbolRange{Start: 0x2000, End: 0x2100},
	}
}

func (b *fakeBlkThrotlBPF) Close() error {
	b.closeCalls++
	return b.closeErr
}

func emptyBlkThrotlContainerSource() (map[string]*pod.Container, error) {
	return nil, nil
}

type blockingBlkThrotlBPF struct {
	fakeBlkThrotlBPF
	mu             sync.Mutex
	dumpStarted    chan struct{}
	releaseDump    chan struct{}
	closed         chan struct{}
	dumpOnce       sync.Once
	closeOnce      sync.Once
	closedFlag     bool
	readAfterClose bool
}

func newBlockingBlkThrotlBPF() *blockingBlkThrotlBPF {
	return &blockingBlkThrotlBPF{
		fakeBlkThrotlBPF: fakeBlkThrotlBPF{
			mapID: 7,
			value: make([]byte, binary.Size(ioControlStatus{})),
		},
		dumpStarted: make(chan struct{}),
		releaseDump: make(chan struct{}),
		closed:      make(chan struct{}),
	}
}

func (b *blockingBlkThrotlBPF) markRead() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closedFlag {
		b.readAfterClose = true
	}
}

func (b *blockingBlkThrotlBPF) DumpMapByName(
	name string,
) ([]bpf.MapItem, error) {
	b.markRead()
	if name == blkThrotlWaitAggregateMap {
		b.dumpOnce.Do(func() { close(b.dumpStarted) })
		<-b.releaseDump
		return nil, nil
	}
	if name == blkThrotlPendingMap {
		return nil, nil
	}
	if name == blkThrotlRetiredMap {
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected map %q", name)
}

func (b *blockingBlkThrotlBPF) dumpBatch(
	mapID uint32,
) ([]bpf.MapItem, error) {
	if mapID != b.MapIDByName(blkThrotlWaitAggregateMap) {
		return nil, fmt.Errorf("unexpected batch map ID %d", mapID)
	}
	return b.DumpMapByName(blkThrotlWaitAggregateMap)
}

func (b *blockingBlkThrotlBPF) MapIDByName(name string) uint32 {
	b.markRead()
	return b.fakeBlkThrotlBPF.MapIDByName(name)
}

func (b *blockingBlkThrotlBPF) ReadMap(
	mapID uint32,
	key []byte,
) ([]byte, error) {
	b.markRead()
	return b.fakeBlkThrotlBPF.ReadMap(mapID, key)
}

func (b *blockingBlkThrotlBPF) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeOnce.Do(func() {
		b.closedFlag = true
		b.closeCalls++
		close(b.closed)
	})
	return b.closeErr
}

func (b *blockingBlkThrotlBPF) closeState() (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeCalls, b.readAfterClose
}

// Object contracts, status decoding, and required attachment paths.

func requireBlkThrotlTestSession(t *testing.T, collector *blkThrotlTracing) {
	t.Helper()
	require.Eventually(t, func() bool {
		collector.mu.Lock()
		defer collector.mu.Unlock()
		if collector.session != nil {
			if object, ok := collector.session.object.(ioControlTestBPF); ok {
				collector.session.hashReader.lookupBatch = object.dumpBatch
			}
		}
		return collector.session != nil
	}, time.Second, time.Millisecond, "blk_throtl session was not published")
}

func blkThrotlTestStatusValue(value []byte, changes *[]uint64) []byte {
	if len(value) != binary.Size(ioControlStatus{}) || len(*changes) == 0 {
		return value
	}
	result := append([]byte(nil), value...)
	binary.LittleEndian.PutUint64(result[8:], nextBlkThrotlTestDeviceChange(changes))
	return result
}

func TestBlkThrotlObjectContract(t *testing.T) {
	object := filepath.Join("..", "..", "bpf",
		"blk_throtl_tracing.o")
	spec, err := ebpf.LoadCollectionSpec(object)
	require.NoError(t, err)

	wantPrograms := []string{
		"io_control_device_removed",
		"io_control_device_changed",
		"io_control_disk_added",
		"kprobe_blk_throtl_pd_free",
		"kretprobe_blk_throtl_pop_queued",
		"kprobe_blk_throtl_pop_queued",
		"kprobe_blk_throtl_add_bio_tg",
	}
	programNames := make([]string, 0, len(spec.Programs))
	for name := range spec.Programs {
		programNames = append(programNames, name)
	}
	require.ElementsMatch(t, wantPrograms, programNames)

	maps := []ioControlTestMapContract{
		{
			name: "blk_throtl_blkg_owner_map", typeID: ebpf.Hash, keySize: 8,
			valueSize: 24, maxEntries: blkThrotlObjectEntries,
		},
		{
			name: "blk_throtl_pending_map", typeID: ebpf.Hash, keySize: 8,
			valueSize: 8, maxEntries: blkThrotlPendingEntries,
		},
		{
			name: "blk_throtl_pop_call_map", typeID: ebpf.Hash, keySize: 8,
			valueSize: 1, maxEntries: blkThrotlObjectEntries,
		},
		{
			name: "blk_throtl_wait_agg_map", typeID: ebpf.PerCPUHash, keySize: 32,
			valueSize: 8, maxEntries: blkThrotlObjectEntries * 2,
			flags: unix.BPF_F_NO_PREALLOC,
		},
		{
			name: blkThrotlRetiredMap, typeID: ebpf.Hash, keySize: 24,
			valueSize: 1, maxEntries: blkThrotlObjectEntries,
		},
		{
			name: ioControlStatusMap, typeID: ebpf.Array, keySize: 4,
			valueSize: 16, maxEntries: 1,
		},
	}
	for _, expected := range maps {
		requireIOControlTestMap(t, spec, expected)
	}
	require.NoError(t, spec.RewriteConstants(map[string]any{
		"blk_throtl_tracked_caller_start": uint64(1),
		"blk_throtl_tracked_caller_end":   uint64(2),
		"blk_throtl_ignored_caller_start": uint64(3),
		"blk_throtl_ignored_caller_end":   uint64(4),
	}))
}

func TestBlkThrotlLifecycleRetainsCgroupCounters(t *testing.T) {
	source := ioControlBPFSource(t, "blk_throtl_tracing.c")
	freeStart := strings.Index(source, "SEC(\"kprobe/throtl_pd_free\")")
	require.GreaterOrEqual(t, freeStart, 0)
	program := `
#include <assert.h>
#include <stdbool.h>
#include <stdint.h>
#include <string.h>
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
typedef uint64_t u64;
typedef uint32_t u32;
typedef uint8_t u8;
#define SEC(name)
#define BPF_CORE_READ(src, field) ((src)->field)
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
#define COMPAT_BPF_NOEXIST 1
#define BLK_THROTL_ENOENT 2
#define BLK_THROTL_EEXIST 17
struct pt_regs { u64 arg1; };
struct blkg_policy_data { void *blkg; };
` + ioControlBPFSource(t, "include/bpf_iocontrol.h") +
		ioControlTestSourceSection(t, source,
			"struct blk_throtl_blkg_owner {", "_Static_assert(sizeof(struct blk_throtl_blkg_owner)") + `
static int blk_throtl_blkg_owner_map, blk_throtl_retired_map;
static struct blk_throtl_blkg_owner owner = {
    .css = 0x100, .css_serial = 0x200, .major = 8, .minor = 16,
};
static struct io_control_status status;
static struct io_control_status *io_control_status_value(void) { return &status; }
static struct io_control_retired_key marker;
static bool owner_live;
static long update_ret;
static u32 attempts;
static void *bpf_map_lookup_elem(void *map, const void *key)
{
    assert(map == &blk_throtl_blkg_owner_map && *(const u64 *)key == 0x300);
    return owner_live ? &owner : NULL;
}
static long bpf_map_update_elem(void *map, const void *key, const void *value, u64 flags)
{
    assert(map == &blk_throtl_retired_map && flags == COMPAT_BPF_NOEXIST);
    assert(*(const u8 *)value == 1);
    marker = *(const struct io_control_retired_key *)key;
    attempts++;
    return update_ret;
}
static long bpf_map_delete_elem(void *map, const void *key)
{
    assert(map == &blk_throtl_blkg_owner_map && *(const u64 *)key == 0x300);
    assert(owner_live && "pd_free must delete only its owner, not cumulative rows");
    owner_live = false;
    return 0;
}
` + ioControlTestSourceSection(t, source,
		"static __always_inline void blk_throtl_fail", "static __always_inline bool blk_throtl_delete_pending") +
		ioControlTestSourceSection(t, source,
			"static __always_inline bool blk_throtl_delete_owner",
			"static __always_inline bool blk_throtl_wait_bio_operation") + source[freeStart:] + `
int main(void)
{
    struct blkg_policy_data pd = { .blkg = (void *)0x300 };
    struct pt_regs ctx = { .arg1 = (u64)&pd };
    int errors[] = { 0, -17, -7 };
    for (int extension = 0; extension < 2; extension++) {
        for (u32 i = 0; i < sizeof(errors) / sizeof(errors[0]); i++) {
            owner_live = true;
            status.failure = 0;
            update_ret = extension ? (long)(u32)errors[i] : errors[i];
            kprobe_blk_throtl_pd_free(&ctx);
            assert(!owner_live);
            assert(marker.css == owner.css && marker.css_serial == owner.css_serial);
            assert(marker.major == owner.major && marker.minor == owner.minor);
            if (errors[i] == -7) {
                assert(status.failure >> 32 == BLK_THROTL_FAILURE_RETIRE);
                assert((int)(u32)status.failure == -7);
            } else {
                assert(!status.failure);
            }
        }
    }
    assert(attempts == 6);
    kprobe_blk_throtl_pd_free(&ctx);
    assert(attempts == 6);
    return 0;
}
`
	executable := compileIOControlTestC(
		t,
		"clang",
		"blk-throtl-retirement",
		program,
		"-I",
		filepath.Join("..", "..", "bpf", "include"),
		"-static",
	)
	output, err := exec.CommandContext(t.Context(), executable).CombinedOutput()
	require.NoError(t, err, "%s", output)
}

// Startup, permanent-stop, retry, and in-flight cancellation boundaries.

func TestBlkThrotlStartWithHooks(t *testing.T) {
	hooks := testBlkThrotlHooks()
	wantConstants := map[string]any{
		"blk_throtl_tracked_caller_start": uint64(0x1000),
		"blk_throtl_tracked_caller_end":   uint64(0x1100),
		"blk_throtl_ignored_caller_start": uint64(0x2000),
		"blk_throtl_ignored_caller_end":   uint64(0x2100),
	}
	wantOptions, err := ioControlDeviceHooks()
	require.NoError(t, err)
	wantOptions = append(wantOptions, []bpf.AttachOption{
		{ProgramName: "kprobe_blk_throtl_pd_free", Symbol: "throtl_pd_free"},
		{ProgramName: "kretprobe_blk_throtl_pop_queued", Symbol: "throtl_pop_queued"},
		{ProgramName: "kprobe_blk_throtl_pop_queued", Symbol: "throtl_pop_queued"},
		{ProgramName: "kprobe_blk_throtl_add_bio_tg", Symbol: "throtl_add_bio_tg"},
	}...)

	for _, test := range []struct {
		name      string
		sourceErr error
	}{
		{name: "loads rodata and attaches the complete group"},
		{name: "container discovery unavailable", sourceErr: errors.New("kubelet not running")},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := &fakeBlkThrotlBPF{}
			var gotConstants map[string]any
			load := func(_ string, constants map[string]any) (bpf.BPF, error) {
				gotConstants = constants
				return object, nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			tracing := &blkThrotlTracing{}
			done := make(chan error, 1)
			go func() {
				done <- tracing.startWithAttribution(
					ctx,
					load,
					hooks,
					1,
					func() (map[string]*pod.Container, error) { return nil, test.sourceErr },
					time.Millisecond,
				)
			}()
			requireBlkThrotlTestSession(t, tracing)
			cancel()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("blk_throtl session did not stop")
			}
			require.Equal(t, wantConstants, gotConstants)
			require.Equal(t, wantOptions, object.options)
			require.Equal(t, 1, object.closeCalls)
			require.Nil(t, tracing.session)
		})
	}

	t.Run("closes object after group attach failure", func(t *testing.T) {
		object := &fakeBlkThrotlBPF{attachErr: fmt.Errorf("attach failed: %w", unix.EPERM)}
		load := func(_ string, constants map[string]any) (bpf.BPF, error) {
			require.Equal(t, wantConstants, constants)
			return object, nil
		}

		tracing := &blkThrotlTracing{}
		err := tracing.startWithAttribution(
			t.Context(),
			load,
			hooks,
			1,
			emptyBlkThrotlContainerSource,
			time.Millisecond,
		)
		require.ErrorContains(t, err, "attach blk-throttle hooks")
		require.ErrorIs(t, err, object.attachErr)
		require.Equal(t, wantOptions, object.options)
		require.Equal(t, 1, object.closeCalls)
		require.Nil(t, tracing.session)
	})

	for _, phase := range []string{"load", "attach"} {
		t.Run("cancellation during replacement "+phase, func(t *testing.T) {
			failed := &fakeBlkThrotlBPF{attachErr: unix.EIO}
			replacement := &fakeBlkThrotlBPF{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if phase == "attach" {
				replacement.onAttach = cancel
			}
			loads := 0
			load := func(string, map[string]any) (bpf.BPF, error) {
				loads++
				if loads == 1 {
					return failed, nil
				}
				if failed.closeCalls != 1 {
					return nil, errors.New("previous attachment was not closed before replacement")
				}
				if phase == "load" {
					cancel()
				}
				return replacement, nil
			}
			tracing := &blkThrotlTracing{}
			// A canceled attempt must exit without acquiring the publication lock.
			tracing.mu.Lock()
			defer tracing.mu.Unlock()
			done := make(chan error, 1)
			go func() {
				done <- tracing.startWithAttribution(
					ctx,
					load,
					hooks,
					1,
					emptyBlkThrotlContainerSource,
					time.Millisecond,
				)
			}()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("canceled attachment attempted to publish a session")
			}
			require.Equal(t, 2, loads)
			require.Equal(t, wantOptions, failed.options)
			if phase == "load" {
				require.Empty(t, replacement.options)
			} else {
				require.Equal(t, wantOptions, replacement.options)
			}
			require.Equal(t, 1, replacement.closeCalls)
			require.Nil(t, tracing.session)
		})
	}

	t.Run("close failure prevents another attachment", func(t *testing.T) {
		object := &fakeBlkThrotlBPF{attachErr: unix.EIO, closeErr: unix.EBUSY}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		object.onAttach = cancel
		loads := 0
		load := func(string, map[string]any) (bpf.BPF, error) {
			loads++
			return object, nil
		}
		tracing := &blkThrotlTracing{}
		err := tracing.startWithAttribution(
			ctx,
			load,
			hooks,
			1,
			emptyBlkThrotlContainerSource,
			time.Millisecond,
		)
		require.ErrorIs(t, err, types.ErrTracingStopped)
		require.ErrorIs(t, err, unix.EIO)
		require.ErrorIs(t, err, unix.EBUSY)
		require.Equal(t, 1, loads)
		require.Equal(t, wantOptions, object.options)
		require.Equal(t, 1, object.closeCalls)
		require.Nil(t, tracing.session)
	})
}

// A terminal status read must release Start only after the BPF object is closed.
func TestBlkThrotlFatalUpdateStopsStart(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   ioControlStatus
		reason   string
		closeErr error
	}{
		{
			name: "duplicate pending", reason: "duplicate admission",
			status: ioControlTestStatus(blkThrotlFailurePendingCollision, -int32(unix.EEXIST)),
		},
		{
			name: "unknown caller", reason: "unrecognized throtl_add_bio_tg caller",
			status: ioControlTestStatus(blkThrotlFailureUnknownCaller, 0),
		},
		{
			name: "map capacity", reason: "capacity exhausted (10240 entries)",
			status: ioControlTestStatus(blkThrotlFailurePendingInsert, -int32(unix.E2BIG)),
		},
		{
			name: "pending insertion", reason: "pending insertion failed: helper returned -12",
			status: ioControlTestStatus(blkThrotlFailurePendingInsert, -int32(unix.ENOMEM)),
		},
		{
			name: "pending deletion", reason: "pending deletion failed: helper returned -5",
			status: ioControlTestStatus(blkThrotlFailurePendingDelete, -int32(unix.EIO)),
		},
		{
			name: "owner creation", reason: "blkg owner",
			status: ioControlTestStatus(blkThrotlFailureOwner, -int32(unix.E2BIG)),
		},
		{
			name: "aggregate allocation", reason: "helper returned -12",
			status: ioControlTestStatus(blkThrotlFailureAggregate, -int32(unix.ENOMEM)),
		},
		{
			name: "retirement marker capacity", reason: "helper returned -7",
			status: ioControlTestStatus(blkThrotlFailureRetire, -int32(unix.E2BIG)),
		},
		{
			name: "pop state", reason: "helper returned -17",
			status: ioControlTestStatus(blkThrotlFailurePopState, -int32(unix.EEXIST)),
		},
		{
			name: "time rollback", reason: "wait end precedes",
			status: ioControlTestStatus(blkThrotlFailureTimeRollback, 0),
		},
		{
			name: "unknown stop reason", reason: "unknown BPF stop reason 99",
			status: ioControlTestStatus(99, 0),
		},
		{
			name: "cleanup failure", reason: "duplicate admission",
			status:   ioControlTestStatus(blkThrotlFailurePendingCollision, -int32(unix.EEXIST)),
			closeErr: errors.New("close failed"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := newBlockingBlkThrotlBPF()
			object.value = ioControlTestBytes(test.status)
			object.closeErr = test.closeErr
			close(object.releaseDump)
			collector := &blkThrotlTracing{}
			startErrors := make(chan error, 1)
			ctx, cancelRun := context.WithCancel(t.Context())
			t.Cleanup(cancelRun)
			go func() {
				startErrors <- collector.startWithAttribution(
					ctx,
					func(string, map[string]any) (bpf.BPF, error) { return object, nil },
					testBlkThrotlHooks(),
					1,
					emptyBlkThrotlContainerSource,
					time.Millisecond,
				)
			}()
			requireBlkThrotlTestSession(t, collector)
			metrics, err := collector.Update()
			require.Nil(t, metrics)
			require.ErrorIs(t, err, types.ErrTracingStopped)
			require.ErrorContains(t, err, test.reason)
			if int32(test.status.Failure) != 0 {
				require.ErrorContains(t, err, fmt.Sprintf("helper returned %d", int32(test.status.Failure)))
			}
			if test.status.Failure>>32 != uint64(blkThrotlFailurePendingInsert) || int32(test.status.Failure) != -int32(unix.E2BIG) {
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

func TestBlkThrotlDeviceChangeAllowsSessionRestart(t *testing.T) {
	object := newBlockingBlkThrotlBPF()
	object.deviceChanges = []uint64{0, 1}
	object.value = ioControlTestBytes(ioControlTestStatus(blkThrotlFailureOwner, 0))
	close(object.releaseDump)
	collector := &blkThrotlTracing{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- collector.startWithAttribution(
			ctx,
			func(string, map[string]any) (bpf.BPF, error) { return object, nil },
			testBlkThrotlHooks(),
			1,
			emptyBlkThrotlContainerSource,
			time.Millisecond,
		)
	}()
	requireBlkThrotlTestSession(t, collector)

	metrics, err := collector.Update()
	require.ErrorIs(t, err, errIOControlDevicesChanged)
	require.NotErrorIs(t, err, types.ErrTracingStopped)
	require.Nil(t, metrics)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("device change did not release blk_throtl Start")
	}
	closes, readAfterClose := object.closeState()
	require.Equal(t, 1, closes)
	require.False(t, readAfterClose)
	require.Nil(t, collector.session)

	replacement := newBlockingBlkThrotlBPF()
	close(replacement.releaseDump)
	go func() {
		done <- collector.startWithAttribution(
			ctx,
			func(string, map[string]any) (bpf.BPF, error) { return replacement, nil },
			testBlkThrotlHooks(),
			1,
			emptyBlkThrotlContainerSource,
			time.Millisecond,
		)
	}()
	requireBlkThrotlTestSession(t, collector)
	metrics, err = collector.Update()
	require.NoError(t, err)
	require.Empty(t, metrics)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("replacement blk_throtl session did not stop")
	}
	closes, readAfterClose = replacement.closeState()
	require.Equal(t, 1, closes)
	require.False(t, readAfterClose)
}

func TestBlkThrotlIntervalCancelWaitsForInFlightCollection(t *testing.T) {
	object := newBlockingBlkThrotlBPF()
	load := func(string, map[string]any) (bpf.BPF, error) {
		return object, nil
	}
	hooks := testBlkThrotlHooks()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tracing := &blkThrotlTracing{}
	startDone := make(chan error, 1)
	go func() {
		startDone <- tracing.startWithAttribution(
			ctx,
			load,
			hooks,
			1,
			emptyBlkThrotlContainerSource,
			time.Millisecond,
		)
	}()

	requireBlkThrotlTestSession(t, tracing)
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
		"a canceled generation must not commit its baseline")

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

// Run with TEST_INTEGRATION=true after building bpf/blk_throtl_tracing.o.
func TestBlkThrotlStartAttachSmoke(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to run the real blk_throtl attach smoke")
	}
	require.NoError(t, bpf.Init(&bpf.Option{}))
	t.Cleanup(bpf.Shutdown)
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join("..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = oldDir })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tracing := &blkThrotlTracing{}
	done := make(chan error, 1)
	go func() { done <- tracing.Start(ctx) }()
	var err error
	isStopped := false
	require.Eventually(t, func() bool {
		select {
		case err = <-done:
			isStopped = true
			return true
		default:
		}
		tracing.mu.Lock()
		defer tracing.mu.Unlock()
		return tracing.session != nil
	}, 30*time.Second, 10*time.Millisecond, "blk_throtl session was not published")
	cancel()
	if !isStopped {
		select {
		case err = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("blk_throtl session did not stop")
		}
	}
	if errors.Is(err, types.ErrNotSupported) && os.Getenv("TEST_BLK_THROTL_REQUIRED") != "1" {
		t.Skip(err)
	}
	require.NoError(t, err)
	require.Nil(t, tracing.session)
}

// Runtime hook selection and incomplete-profile rejection.

func TestResolveBlkThrotlHooks(t *testing.T) {
	tests := []struct {
		name    string
		profile map[string]symbol.KsymbolRange
		want    blkThrotlHooks
	}{
		{
			name: "modern tracked caller is preferred",
			profile: blkThrotlHookProfileForTest(map[string]symbol.KsymbolRange{
				"__blk_throtl_bio": {Start: 0x1000, End: 0x1100},
				"blk_throtl_bio":   {Start: 0x1800, End: 0x1900},
			}),
			want: blkThrotlHooks{
				trackedRange: symbol.KsymbolRange{Start: 0x1000, End: 0x1100},
				ignoredRange: symbol.KsymbolRange{Start: 0x2000, End: 0x2100},
			},
		},
		{
			name: "legacy tracked caller is the fallback",
			profile: blkThrotlHookProfileForTest(map[string]symbol.KsymbolRange{
				"blk_throtl_bio": {Start: 0x1000, End: 0x1100},
			}),
			want: blkThrotlHooks{
				trackedRange: symbol.KsymbolRange{Start: 0x1000, End: 0x1100},
				ignoredRange: symbol.KsymbolRange{Start: 0x2000, End: 0x2100},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries := [][]string{
				{"throtl_add_bio_tg", "throtl_pop_queued", "throtl_pd_free"},
				{"__blk_throtl_bio", "blk_throtl_bio", "tg_dispatch_one_bio"},
			}
			modes := []symbol.KsymbolLookupMode{symbol.KsymbolLookupDefault, symbol.KsymbolLookupRange}
			calls := 0
			got, err := resolveBlkThrotlHooks(
				func(names []string, mode symbol.KsymbolLookupMode) (map[string]symbol.KsymbolRange, error) {
					require.Less(t, calls, len(queries))
					require.ElementsMatch(t, queries[calls], names)
					require.Equal(t, modes[calls], mode)
					calls++
					return test.profile, nil
				},
			)
			require.NoError(t, err)
			require.Equal(t, len(queries), calls)
			require.Equal(t, &test.want, got)
		})
	}
}

func TestResolveBlkThrotlHooksRejectsInvalidProfile(t *testing.T) {
	tests := []struct {
		name         string
		missing      string
		invalidRange string
		hasOverlap   bool
		wantInError  string
	}{
		{name: "tracked caller", missing: "blk_throtl_bio", wantInError: "tracked caller"},
		{name: "ignored caller", missing: "tg_dispatch_one_bio", wantInError: "ignored caller"},
		{name: "add hook", missing: "throtl_add_bio_tg", wantInError: "throtl_add_bio_tg"},
		{name: "pop hook", missing: "throtl_pop_queued", wantInError: "throtl_pop_queued"},
		{name: "pd free hook", missing: "throtl_pd_free", wantInError: "throtl_pd_free"},
		{name: "tracked range end", invalidRange: blkThrotlLegacyTrackedCaller, wantInError: blkThrotlLegacyTrackedCaller},
		{name: "ignored range end", invalidRange: blkThrotlIgnoredCaller, wantInError: blkThrotlIgnoredCaller},
		{name: "overlapping callers", hasOverlap: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := blkThrotlHookProfileForTest(map[string]symbol.KsymbolRange{
				"blk_throtl_bio": {Start: 0x1000, End: 0x1100},
			})
			delete(profile, test.missing)
			if test.invalidRange != "" {
				value := profile[test.invalidRange]
				value.End = 0
				profile[test.invalidRange] = value
			}
			if test.hasOverlap {
				profile[blkThrotlIgnoredCaller] = symbol.KsymbolRange{Start: 0x1080, End: 0x1180}
			}
			got, err := resolveBlkThrotlHooks(
				func([]string, symbol.KsymbolLookupMode) (map[string]symbol.KsymbolRange, error) {
					return profile, nil
				},
			)
			require.Error(t, err)
			if test.wantInError != "" {
				require.ErrorContains(t, err, test.wantInError)
			}
			require.Nil(t, got)
		})
	}
}

func TestResolveBlkThrotlHooksPropagatesProfileFailure(
	t *testing.T,
) {
	failure := errors.New("scan failed")
	for failAt := 1; failAt <= 2; failAt++ {
		calls := 0
		got, err := resolveBlkThrotlHooks(
			func([]string, symbol.KsymbolLookupMode) (map[string]symbol.KsymbolRange, error) {
				calls++
				if calls == failAt {
					return nil, failure
				}
				return nil, nil
			},
		)
		require.ErrorIs(t, err, failure)
		require.Nil(t, got)
	}
}

func TestResolveBlkThrotlHooksReadsCurrentRanges(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	path := procfs.Path("kallsyms")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	load := func() (*blkThrotlHooks, error) {
		return resolveBlkThrotlHooks(func(
			names []string,
			mode symbol.KsymbolLookupMode,
		) (map[string]symbol.KsymbolRange, error) {
			if mode == symbol.KsymbolLookupDefault {
				return blkThrotlHookProfileForTest(nil), nil
			}
			return symbol.KsymbolSearchAddr(names, mode)
		})
	}

	hooks, err := load()
	require.Nil(t, hooks)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NotErrorIs(t, err, types.ErrNotSupported,
		"a symbol file read failure must remain retryable")

	// A successful read with missing required hooks proves unsupported.
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	hooks, err = load()
	require.Nil(t, hooks)
	require.ErrorIs(t, err, types.ErrNotSupported)

	const kallsyms = `0000000000001000 t blk_throtl_bio
0000000000001100 t tracked_end
0000000000002000 t tg_dispatch_one_bio
0000000000002100 t ignored_end
0000000000003000 t throtl_add_bio_tg
0000000000004000 t throtl_pop_queued
0000000000005000 t throtl_pd_free
`
	require.NoError(t, os.WriteFile(path, []byte(kallsyms), 0o600))
	hooks, err = load()
	require.NoError(t, err)
	require.NotNil(t, hooks)
}

func blkThrotlHookProfileForTest(
	extra map[string]symbol.KsymbolRange,
) map[string]symbol.KsymbolRange {
	profile := map[string]symbol.KsymbolRange{
		"throtl_add_bio_tg":   {Start: 0x3000},
		"throtl_pop_queued":   {Start: 0x4000},
		"throtl_pd_free":      {Start: 0x5000},
		"tg_dispatch_one_bio": {Start: 0x2000, End: 0x2100},
	}
	for name, value := range extra {
		profile[name] = value
	}
	return profile
}
