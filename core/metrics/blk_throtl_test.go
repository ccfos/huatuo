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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
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
	}
}

func (b *fakeBlkThrotlCollectorBPF) DumpMapByName(
	name string,
) ([]bpf.MapItem, error) {
	if name != blkThrotlWaitAggregateMap {
		return nil, fmt.Errorf("unexpected map %q", name)
	}
	b.dumpCalls++
	if len(b.dumps) == 0 {
		return nil, fmt.Errorf("unexpected extra %s dump", name)
	}
	result := b.dumps[0]
	b.dumps = b.dumps[1:]
	return result.items, result.err
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
		configure func(*fakeBlkThrotlCollectorBPF)
		failure   blkThrotlTestDump
	}{
		{
			name:    "initial aggregate dump",
			initial: true,
			failure: blkThrotlTestDump{err: errors.New("aggregate dump failed")},
		},
		{
			name:    "aggregate dump",
			failure: blkThrotlTestDump{err: errors.New("aggregate dump failed")},
		},
		{
			name:    "aggregate traversal",
			failure: blkThrotlTestDump{items: []bpf.MapItem{current, current}},
		},
		{
			name: "health status read",
			configure: func(object *fakeBlkThrotlCollectorBPF) {
				object.statusErr = errors.New("status read failed")
				object.statusErrAtCall = object.statusCalls + 2
			},
			failure: blkThrotlTestDump{items: []bpf.MapItem{current}},
		},
		{
			name: "entry status read",
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

			for range 2 {
				if test.configure != nil {
					test.configure(object)
				}
				object.setDumps(test.failure, test.failure, test.failure)
				metrics, err := collector.Update()
				require.Error(t, err)
				require.Empty(t, metrics)
				require.Nil(t, context.Cause(collector.session.breaker))
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
	baseline := blkThrotlTestItem(key, stableBlkThrotlTestLane(10, 1_000))
	current := blkThrotlTestItem(key, stableBlkThrotlTestLane(15, 2_000))
	for _, test := range []struct {
		name      string
		changes   []uint64
		configure func(*fakeBlkThrotlCollectorBPF)
	}{
		{name: "before snapshot", changes: []uint64{1}},
		{
			name: "BPF error during transition", changes: []uint64{0, 1},
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
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{current}})
			metrics, err := collector.Update()
			require.ErrorIs(t, err, errIOControlDevicesChanged)
			require.NotErrorIs(t, err, types.ErrTracingStopped)
			if test.name == "BPF error during transition" {
				requireBlkThrotlHostAndOtherMetrics(t, metrics, "sda", "read", 0, 0)
			} else {
				require.Nil(t, metrics)
			}
			require.ErrorIs(t, context.Cause(collector.session.breaker), errIOControlDevicesChanged)
			require.Equal(t, previous, collector.session.previous)
		})
	}
}

func TestBlkThrotlDeviceChangeZerosCapturedInterval(t *testing.T) {
	for _, count := range []uint64{1, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			key := blkThrotlTestKey{css: 1, major: 4095, minor: 1, operation: 0}
			object := newFakeBlkThrotlCollectorBPF()
			collector := newBlkThrotlTestCollector(t, object, 1)
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(100, 1_000)),
			}})
			_, err := collector.Update()
			require.NoError(t, err)
			previous := collector.session.previous

			object.deviceChanges = []uint64{0, 1}
			object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
				blkThrotlTestItem(key, stableBlkThrotlTestLane(count, 10)),
			}})
			metrics, err := collector.Update()
			require.ErrorIs(t, err, errIOControlDevicesChanged)
			requireBlkThrotlHostAndOtherMetrics(t, metrics, "sda", "read", 0, 0)
			require.ErrorIs(t, context.Cause(collector.session.breaker), errIOControlDevicesChanged)

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

func TestBlkThrotlIntervalRejectsWaitWithoutDelayedIO(t *testing.T) {
	key := blkThrotlTestKey{css: 1, major: 4095, minor: 1, operation: 0}

	t.Run("delta", func(t *testing.T) {
		object := newFakeBlkThrotlCollectorBPF()
		collector := newBlkThrotlTestCollector(t, object, 1)
		object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
			blkThrotlTestItem(key,
				stableBlkThrotlTestLane(10, 1_000)),
		}})
		_, err := collector.Update()
		require.NoError(t, err)

		object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
			blkThrotlTestItem(key,
				stableBlkThrotlTestLane(10, 1_100)),
		}})
		_, err = collector.Update()
		require.Error(t, err)

		object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
			blkThrotlTestItem(key,
				stableBlkThrotlTestLane(15, 2_000)),
		}})
		metrics, err := collector.Update()
		require.ErrorIs(t, err, metric.ErrNoData)
		require.Empty(t, metrics)

		object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
			blkThrotlTestItem(key,
				stableBlkThrotlTestLane(17, 2_800)),
		}})
		metrics, err = collector.Update()
		require.NoError(t, err)
		requireBlkThrotlHostAndOtherMetrics(t, metrics,
			"sda", "read", 2, 4)
	})
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

func TestBlkThrotlAttributionFailuresRebuildBaseline(t *testing.T) {
	t.Run("aggregate snapshot failure", func(t *testing.T) {
		collector, object, source, key := newBlkThrotlAttributionFailureFixture(t)
		source.containers = nil
		aggregateFailure := errors.New("aggregate dump failed")
		object.setDumps(blkThrotlTestDump{err: aggregateFailure})
		metrics, err := collector.Update()
		require.ErrorIs(t, err, aggregateFailure)
		require.Empty(t, metrics)
		require.Nil(t, collector.session.previous)

		object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
			blkThrotlTestItem(key,
				stableBlkThrotlTestLane(13, 1_900)),
		}})
		metrics, err = collector.Update()
		require.ErrorIs(t, err, metric.ErrNoData)
		require.Empty(t, metrics)

		container := newIOControlAttributionTestContainer("new-container", 0x11, time.Unix(200, 0))
		source.containers = map[string]*pod.Container{container.ID: container}
		object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
			blkThrotlTestItem(key,
				stableBlkThrotlTestLane(15, 2_700)),
		}})
		metrics, err = collector.Update()
		require.NoError(t, err)
		requireBlkThrotlHostAndContainerInterval(t, metrics, 2, 4)
	})
}

func newBlkThrotlAttributionFailureFixture(t *testing.T) (
	*blkThrotlTracing,
	*fakeBlkThrotlCollectorBPF,
	*ioControlAttributionTestSource,
	blkThrotlTestKey,
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
	object := newFakeBlkThrotlCollectorBPF()
	collector := newBlkThrotlAttributedTestCollector(t, object, source)
	key := blkThrotlTestKey{
		css: 0x11, major: 4095, minor: 1, operation: 0,
	}
	object.setDumps(blkThrotlTestDump{items: []bpf.MapItem{
		blkThrotlTestItem(key,
			stableBlkThrotlTestLane(10, 1_000)),
	}})
	metrics, err := collector.Update()
	require.NoError(t, err)
	requireBlkThrotlHostAndContainerInterval(t, metrics, 10, 1)
	return collector, object, source, key
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
	closeErr      error
	mapID         uint32
	value         []byte
	readErr       error
	deviceChanges []uint64
	closeCalls    int
}

func (b *fakeBlkThrotlBPF) AttachWithOptions(options []bpf.AttachOption) error {
	b.options = append(b.options, options...)
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

func TestBlkThrotlAttachHooks(t *testing.T) {
	want := append(ioControlDeviceHooks(), []bpf.AttachOption{
		{ProgramName: "kprobe_blk_throtl_pd_free", Symbol: "throtl_pd_free"},
		{ProgramName: "kretprobe_blk_throtl_pop_queued", Symbol: "throtl_pop_queued"},
		{ProgramName: "kprobe_blk_throtl_pop_queued", Symbol: "throtl_pop_queued"},
		{ProgramName: "kprobe_blk_throtl_add_bio_tg", Symbol: "throtl_add_bio_tg"},
	}...)

	object := &fakeBlkThrotlBPF{}
	require.NoError(t, attachBlkThrotlHooks(object))
	require.Equal(t, want, object.options)

	failing := &fakeBlkThrotlBPF{attachErr: errors.New("attach failed")}
	err := attachBlkThrotlHooks(failing)
	require.ErrorContains(t, err, "attach blk-throttle hooks")
	require.ErrorIs(t, err, failing.attachErr)
	require.Equal(t, want, failing.options)
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
	wantOptions := append(ioControlDeviceHooks(), []bpf.AttachOption{
		{ProgramName: "kprobe_blk_throtl_pd_free", Symbol: "throtl_pd_free"},
		{ProgramName: "kretprobe_blk_throtl_pop_queued", Symbol: "throtl_pop_queued"},
		{ProgramName: "kprobe_blk_throtl_pop_queued", Symbol: "throtl_pop_queued"},
		{ProgramName: "kprobe_blk_throtl_add_bio_tg", Symbol: "throtl_add_bio_tg"},
	}...)

	t.Run("loads rodata and attaches the complete group", func(t *testing.T) {
		object := &fakeBlkThrotlBPF{}
		var gotConstants map[string]any
		load := func(_ string, constants map[string]any) (bpf.BPF, error) {
			gotConstants = constants
			return object, nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		tracing := &blkThrotlTracing{}
		require.NoError(t, tracing.startWithAttribution(
			ctx,
			load,
			hooks,
			1,
			emptyBlkThrotlContainerSource,
		))
		require.Equal(t, wantConstants, gotConstants)
		require.Equal(t, wantOptions, object.options)
		require.Equal(t, 1, object.closeCalls)
		require.Nil(t, tracing.session)
	})

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
		)
		require.ErrorContains(t, err, "attach blk-throttle hooks")
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
				startErrors <- collector.startWithAttribution(ctx,
					func(string, map[string]any) (bpf.BPF, error) { return object, nil },
					testBlkThrotlHooks(), 1, emptyBlkThrotlContainerSource)
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
		done <- collector.startWithAttribution(ctx,
			func(string, map[string]any) (bpf.BPF, error) { return object, nil },
			testBlkThrotlHooks(), 1, emptyBlkThrotlContainerSource)
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
		done <- collector.startWithAttribution(ctx,
			func(string, map[string]any) (bpf.BPF, error) { return replacement, nil },
			testBlkThrotlHooks(), 1, emptyBlkThrotlContainerSource)
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

func TestBlkThrotlStartIgnoresContainerDiscovery(
	t *testing.T,
) {
	hooks := testBlkThrotlHooks()
	object := &fakeBlkThrotlBPF{}
	load := func(string, map[string]any) (bpf.BPF, error) {
		return object, nil
	}
	containerSource := func() (map[string]*pod.Container, error) {
		return nil, errors.New("kubelet not running")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	tracing := &blkThrotlTracing{}
	require.NoError(t, tracing.startWithAttribution(
		ctx,
		load,
		hooks,
		1,
		containerSource,
	))
	require.Nil(t, tracing.session)
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
	cancel()
	tracing := &blkThrotlTracing{}
	err := tracing.Start(ctx)
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
		profile symbol.KsymbolProfile
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
			got, err := resolveBlkThrotlHooks(
				func(addresses, ranges []string) (symbol.KsymbolProfile, error) {
					require.ElementsMatch(t, []string{
						"throtl_add_bio_tg",
						"throtl_pop_queued",
						"throtl_pd_free",
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
		})
	}
}

func TestResolveBlkThrotlHooksRejectsIncompleteProfile(t *testing.T) {
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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := blkThrotlHookProfileForTest(map[string]symbol.KsymbolRange{
				"blk_throtl_bio": {Start: 0x1000, End: 0x1100},
			})
			delete(profile.Addresses, test.missing)
			delete(profile.Ranges, test.missing)
			got, err := resolveBlkThrotlHooks(
				func([]string, []string) (symbol.KsymbolProfile, error) {
					return profile, nil
				},
			)
			require.ErrorContains(t, err, test.wantInError)
			require.Nil(t, got)
		})
	}
}

func TestResolveBlkThrotlHooksRejectsOverlappingCallers(t *testing.T) {
	profile := blkThrotlHookProfileForTest(map[string]symbol.KsymbolRange{
		"blk_throtl_bio": {Start: 0x1000, End: 0x1100},
	})
	profile.Ranges["tg_dispatch_one_bio"] = symbol.KsymbolRange{
		Start: 0x1080,
		End:   0x1180,
	}
	got, err := resolveBlkThrotlHooks(
		func([]string, []string) (symbol.KsymbolProfile, error) {
			return profile, nil
		},
	)
	require.Error(t, err)
	require.Nil(t, got)
}

func TestResolveBlkThrotlHooksPropagatesProfileFailure(
	t *testing.T,
) {
	failure := errors.New("scan failed")
	got, err := resolveBlkThrotlHooks(
		func([]string, []string) (symbol.KsymbolProfile, error) {
			return symbol.KsymbolProfile{}, failure
		},
	)
	require.ErrorIs(t, err, failure)
	require.Nil(t, got)
}

func TestLoadBlkThrotlHooksRetriesSymbolReadFailure(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	path := procfs.Path("kallsyms")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	hooks, err := loadBlkThrotlHooks()
	require.Nil(t, hooks)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NotErrorIs(t, err, types.ErrNotSupported,
		"a symbol file read failure must remain retryable")

	// A successful read with missing required hooks proves unsupported.
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	hooks, err = loadBlkThrotlHooks()
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
	hooks, err = loadBlkThrotlHooks()
	require.NoError(t, err)
	require.NotNil(t, hooks)
}

func blkThrotlHookProfileForTest(
	extra map[string]symbol.KsymbolRange,
) symbol.KsymbolProfile {
	profile := symbol.KsymbolProfile{
		Addresses: map[string]uint64{
			"throtl_add_bio_tg": 0x3000,
			"throtl_pop_queued": 0x4000,
			"throtl_pd_free":    0x5000,
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
