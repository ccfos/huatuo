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

// IOLatency interval, attribution and collection tests use map-boundary fakes.
package collector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Statistics, attribution, and collection lifecycle.

// Map-read and container cleanup fixtures.

type ioLatencyTestDump struct {
	items []bpf.MapItem
	err   error
}

type fakeIOLatencyMapBPF struct {
	bpf.BPF
	maps           map[string][]bpf.MapItem
	errors         map[string]error
	missingMap     string
	statusHook     func()
	dumpHook       func(string)
	statusValue    []byte
	devicesChanged uint64
	dumps          []ioLatencyTestDump
}

type fakeIOLatencyContainerBPF struct {
	fakeIOLatencyMapBPF
	deleteErr    error
	beforeDelete func(uint32, [][]byte) error
}

func (b *fakeIOLatencyMapBPF) MapIDByName(name string) uint32 {
	if b.missingMap == name {
		return 0
	}
	switch name {
	case blkDiskBucketMap:
		return 1
	case blkContainerBucketMap:
		return 2
	case ioControlStatusMap:
		return 3
	default:
		return 0
	}
}

func (b *fakeIOLatencyMapBPF) ReadMap(id uint32, key []byte) ([]byte, error) {
	if id != 3 || !bytes.Equal(key, bytesutil.ToBytes(uint32(0))) {
		return nil, fmt.Errorf("unexpected IO session status lookup: map=%d key=%v", id, key)
	}
	if b.statusHook != nil {
		b.statusHook()
	}
	if err := b.errors[ioControlStatusMap]; err != nil {
		return nil, err
	}
	data := bytesutil.ToBytes(ioControlStatus{})
	if b.statusValue != nil {
		data = bytes.Clone(b.statusValue)
	}
	if b.devicesChanged != 0 && len(data) == 16 {
		binary.LittleEndian.PutUint64(data[8:], b.devicesChanged)
	}
	return data, nil
}

func (b *fakeIOLatencyMapBPF) DumpMap(id uint32) ([]bpf.MapItem, error) {
	if id != 2 {
		return nil, fmt.Errorf("unexpected cgroup counter map ID %d", id)
	}
	if len(b.dumps) != 0 {
		result := b.dumps[0]
		b.dumps = b.dumps[1:]
		return result.items, result.err
	}
	return b.DumpMapByName(blkContainerBucketMap)
}

func (b *fakeIOLatencyMapBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if b.dumpHook != nil {
		b.dumpHook(name)
	}
	if err := b.errors[name]; err != nil {
		return nil, err
	}
	return b.maps[name], nil
}

func (b *fakeIOLatencyContainerBPF) DeleteMapItems(id uint32, keys [][]byte) error {
	if id != 2 {
		return fmt.Errorf("unexpected cgroup counter delete map ID %d", id)
	}
	if b.beforeDelete != nil {
		if err := b.beforeDelete(id, keys); err != nil {
			return err
		}
	}
	if b.deleteErr != nil {
		return b.deleteErr
	}
	for _, key := range keys {
		index := slices.IndexFunc(b.maps[blkContainerBucketMap], func(item bpf.MapItem) bool {
			return bytes.Equal(item.Key, key)
		})
		if index < 0 {
			return unix.ENOENT
		}
		b.maps[blkContainerBucketMap] = slices.Delete(b.maps[blkContainerBucketMap], index, index+1)
	}
	return nil
}

// Collection health, histogram deltas, and rollback boundaries.

// Health is checked on both sides of the map capture. A terminal BPF error
// cancels Start without publishing a partial interval; read errors can retry.
func TestIOLatencyCollectionHealth(t *testing.T) {
	for _, hasSession := range []bool{false, true} {
		t.Run(fmt.Sprintf("no data/has session=%t", hasSession), func(t *testing.T) {
			collector := &iolatencyTracing{}
			if hasSession {
				collector.session = &ioLatencySession{object: &fakeIOLatencyMapBPF{}}
			}
			metrics, err := collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Empty(t, metrics)
			if hasSession {
				require.Nil(t, collector.session.previous)
			}
		})
	}
	for _, test := range []struct {
		name            string
		status          uint64
		value           []byte
		missingMap      bool
		afterCapture    bool
		hasDeviceChange bool
		want            string
	}{
		{name: "request chain exceeds budget", status: 1 << 32, want: "512"},
		{name: "identity cache insertion", status: 3<<32 | 0xfffffff4, want: "identity cache insertion failed"},
		{name: "CSS release publication", status: 4<<32 | 0xfffffff4, want: "release record publication failed"},
		{name: "failure during capture", status: 1 << 32, afterCapture: true, want: "512"},
		{name: "missing status map", missingMap: true, want: "map io_control_status_map is unavailable"},
		{name: "invalid status value", value: []byte{0}, want: "decode io_control_status_map"},
		{name: "device change before capture", hasDeviceChange: true},
		{name: "device change during capture", hasDeviceChange: true, afterCapture: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			row := ioLatencyDiskCounters{Major: 8}
			row.Counters[0].QueuedSize[0] = 5
			object := &fakeIOLatencyMapBPF{
				maps: map[string][]bpf.MapItem{
					blkDiskBucketMap: {ioControlTestMapItem(uint64(100), row)},
				},
			}
			publishFailure := func() {
				if test.hasDeviceChange {
					object.devicesChanged = 1
					return
				}
				object.statusValue = bytesutil.ToBytes(ioControlStatus{Failure: test.status})
				if test.value != nil {
					object.statusValue = test.value
				}
			}
			if test.missingMap {
				object.missingMap = ioControlStatusMap
			}
			if test.afterCapture {
				object.dumpHook = func(name string) {
					if name == blkDiskBucketMap {
						publishFailure()
					}
				}
			} else {
				publishFailure()
			}
			session := &ioLatencySession{object: object, previous: &ioLatencySnapshot{
				capturedAt: time.Unix(100, 0),
				host:       map[ioLatencyHostKey]*ioLatencyCounters{{Major: 8}: {}},
			}, devices: ioControlTestDiskNames(), cancel: cancel}
			collector := &iolatencyTracing{session: session}
			metrics, err := collector.Update()
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
			}
			require.Empty(t, metrics)
			require.Nil(t, session.previous, "failed capture invalidates the complete interval")
			want := types.ErrTracingStopped
			if test.hasDeviceChange {
				want = errIOControlDevicesChanged
				require.NotErrorIs(t, err, types.ErrTracingStopped)
				require.Len(t, object.maps[blkDiskBucketMap], 1)
			}
			require.ErrorIs(t, err, want)
			require.ErrorIs(t, context.Cause(ctx), want)
			collector.withdrawSession()
			metrics, err = collector.Update()
			require.ErrorIs(t, err, metric.ErrNoData)
			require.Empty(t, metrics)
		})
	}
}

func TestIOLatencyIntervalBuckets(t *testing.T) {
	previous := ioLatencyCounters{}
	current := ioLatencyCounters{}
	for stage := range previous.Buckets {
		for bucket := range previous.Buckets[stage] {
			previous.Buckets[stage][bucket] = 100
			current.Buckets[stage][bucket] = 100
		}
	}
	for bucket := range previous.QueuedSize {
		previous.QueuedSize[bucket] = 100
		current.QueuedSize[bucket] = 100 + uint64(bucket+1)
	}
	issuedDeltas := [ioSizeBucketCount]uint64{2, 5, 4, 3, 2, 1}
	for bucket := range previous.IssuedSize {
		previous.IssuedSize[bucket] = 100
		current.IssuedSize[bucket] = 100 + issuedDeltas[bucket]
	}
	current.Buckets[ioLatencyStageQ2D][0] += 2
	current.Buckets[ioLatencyStageQ2D][1] += 3
	current.Buckets[ioLatencyStageQ2D][16]++
	current.Buckets[ioLatencyStageD2C][0]++
	current.Buckets[ioLatencyStageD2C][2] += 2
	current.Buckets[ioLatencyStageQ2G][3] += 4
	delta, monotonic := ioLatencyCountersDelta(&previous, &current)
	if !monotonic {
		t.Fatal("interval counters decreased")
	}
	wantMetricNames := []string{
		"q2d_seconds_bucket",
		"d2c_seconds_bucket",
		"q2g_seconds_bucket",
	}
	if len(ioLatencyStageMetrics) != len(wantMetricNames) {
		t.Fatalf("latency stages = %d, want %d", len(ioLatencyStageMetrics), len(wantMetricNames))
	}
	for index, want := range wantMetricNames {
		if got := ioLatencyStageMetrics[index].name; got != want {
			t.Fatalf("stage %d metric = %q, want %q", index, got, want)
		}
	}
	if got := ioLatencyQueuedSizeMetric.name; got != "queued_io_size_bytes_bucket" {
		t.Fatalf("queued size metric = %q", got)
	}
	if got := ioLatencyIssuedSizeMetric.name; got != "issued_io_size_bytes_bucket" {
		t.Fatalf("issued size metric = %q", got)
	}
	wantSizeBounds := [ioSizeBucketCount]string{
		"4096", "16384", "65536", "262144", "1048576", "+Inf",
	}
	if ioSizeBucketLabels != wantSizeBounds {
		t.Fatalf("queued size bounds = %v, want %v",
			ioSizeBucketLabels, wantSizeBounds)
	}

	container := &pod.Container{
		ID:       "container-id",
		Name:     "container-name",
		Hostname: "container-host",
		Type:     pod.ContainerTypeNormal,
		Qos:      pod.ContainerQosLevelMin,
		Labels:   map[string]any{"HostNamespace": "namespace"},
	}
	for _, scope := range []struct {
		name      string
		container *pod.Container
	}{
		{name: "host"},
		{name: "container", container: container},
	} {
		t.Run(scope.name, func(t *testing.T) {
			metrics := appendIOLatencySeries(
				nil,
				scope.container,
				"sda",
				"read",
				&delta,
			)
			wantCount := ioLatencyStageCount*ioLatencyBucketCount +
				2*ioSizeBucketCount
			if len(metrics) != wantCount {
				t.Fatalf("metric count = %d, want %d", len(metrics),
					wantCount)
			}

			wantQ2D := []float64{
				2, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 6,
			}
			wantD2C := []float64{
				1, 1, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3,
			}
			assertIOLatencyValues(t, metrics[:ioLatencyBucketCount],
				wantQ2D)
			assertIOLatencyValues(t,
				metrics[ioLatencyBucketCount:2*ioLatencyBucketCount],
				wantD2C)
			assertIOLatencyValues(t,
				metrics[2*ioLatencyBucketCount:3*ioLatencyBucketCount],
				[]float64{
					0, 0, 0, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4,
					4, 4, 4, 4,
				})
			assertIOLatencyValues(t,
				metrics[3*ioLatencyBucketCount:3*ioLatencyBucketCount+
					ioSizeBucketCount],
				[]float64{1, 3, 6, 10, 15, 21})
			assertIOLatencyValues(t,
				metrics[3*ioLatencyBucketCount+ioSizeBucketCount:],
				[]float64{2, 7, 11, 14, 16, 17})
		})
	}

	start := time.Unix(100, 250000000)
	end := time.Unix(161, 750000000)
	timestamps := ioLatencyIntervalTimeMetrics(start, end)
	if len(timestamps) != 2 {
		t.Fatalf("timestamp metric count = %d, want 2", len(timestamps))
	}
	if timestamps[0].Value != 100.25 || timestamps[1].Value != 161.75 {
		t.Fatalf("timestamp values = [%v %v], want [100.25 161.75]",
			timestamps[0].Value, timestamps[1].Value)
	}
}

func TestIOLatencySeriesRejectsCounterRollback(t *testing.T) {
	for _, test := range []struct {
		name     string
		rollback func(*ioLatencyCounters, *ioLatencyCounters)
	}{
		{
			name: "latency",
			rollback: func(previous, current *ioLatencyCounters) {
				previous.Buckets[ioLatencyStageQ2D][0] = 2
				current.Buckets[ioLatencyStageQ2D][0] = 1
			},
		},
		{
			name: "queued size",
			rollback: func(previous, current *ioLatencyCounters) {
				previous.QueuedSize[0] = 2
				current.QueuedSize[0] = 1
			},
		},
		{
			name: "issued size",
			rollback: func(previous, current *ioLatencyCounters) {
				previous.IssuedSize[0] = 2
				current.IssuedSize[0] = 1
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := ioLatencyCounters{}
			current := ioLatencyCounters{}
			test.rollback(&previous, &current)
			current.Buckets[ioLatencyStageD2C][0] = 1
			current.Buckets[ioLatencyStageQ2G][0] = 1

			key := ioLatencyHostKey{Major: 8}
			metrics := appendIOLatencyIntervalMetrics(nil,
				&ioLatencySnapshot{host: map[ioLatencyHostKey]*ioLatencyCounters{
					key: &previous,
				}},
				&ioLatencySnapshot{host: map[ioLatencyHostKey]*ioLatencyCounters{
					key: &current,
				}},
				map[[2]uint32]string{{8, 0}: "sda"}, nil,
			)
			if len(metrics) != 0 {
				t.Fatalf("counter rollback exported %d metrics, want none",
					len(metrics))
			}
		})
	}
}

// Public labels aggregate only after raw-identity deltas.

func TestIOLatencyContainerIntervalPublicLabels(t *testing.T) {
	for _, test := range []struct {
		name          string
		secondTotal   uint64
		wantPerBucket float64
	}{
		{name: "both containers have IO", secondTotal: 103, wantPerBucket: 5},
		{name: "one container has no IO", secondTotal: 100, wantPerBucket: 2},
		{name: "one raw row rolls back", secondTotal: 99, wantPerBucket: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := &pod.Container{
				ID: "first", Name: "agent", Hostname: "node",
				Type: pod.ContainerTypeDaemonSet, Qos: pod.ContainerQosLevelMin,
				Labels: map[string]any{"HostNamespace": "namespace"},
			}
			second := *first
			second.ID = "second"
			previous := &ioLatencySnapshot{
				containers: make(map[ioLatencyContainerKey]*ioLatencyCounters),
			}
			current := &ioLatencySnapshot{
				containers: make(map[ioLatencyContainerKey]*ioLatencyCounters),
			}
			cssContainers := make(map[uint64]*pod.Container)
			// Distinct raw baselines must be subtracted before public labels
			// are combined, including when one row is idle or rolls back.
			for index, container := range []*pod.Container{first, &second} {
				before, after := uint64(10), uint64(12)
				if index == 1 {
					before, after = 100, test.secondTotal
				}
				key := ioLatencyContainerKey{Blkcg: uint64(index + 1), Serial: uint64(index + 11), Major: 8}
				cssContainers[key.Blkcg] = container
				old := &ioLatencyCounters{}
				next := *old
				for stage := range old.Buckets {
					for bucket := range old.Buckets[stage] {
						old.Buckets[stage][bucket] = before
						next.Buckets[stage][bucket] = after
					}
				}
				for bucket := range old.QueuedSize {
					old.QueuedSize[bucket], old.IssuedSize[bucket] = before, before
					next.QueuedSize[bucket], next.IssuedSize[bucket] = after, after
				}
				previous.containers[key], current.containers[key] = old, &next
			}

			got := appendIOLatencyIntervalMetrics(nil, previous, current,
				map[[2]uint32]string{{8, 0}: "sda"}, cssContainers)
			var want []*metric.Data
			for _, definition := range ioLatencyStageMetrics {
				for bucket, bound := range ioLatencyBucketLabels {
					want = append(want, metric.NewContainerGaugeData(first,
						definition.name, float64(bucket+1)*test.wantPerBucket,
						definition.help, map[string]string{
							"disk": "sda", "operation": "read", "le": bound,
						}))
				}
			}
			for _, definition := range []ioLatencyBucketMetric{ioLatencyQueuedSizeMetric, ioLatencyIssuedSizeMetric} {
				for bucket, bound := range ioSizeBucketLabels {
					want = append(want, metric.NewContainerGaugeData(first,
						definition.name, float64(bucket+1)*test.wantPerBucket,
						definition.help, map[string]string{
							"disk": "sda", "operation": "read", "le": bound,
						}))
				}
			}
			require.Len(t, got, len(want))
			require.ElementsMatch(t, want, got)
		})
	}
}

func TestIOLatencyContainerIntervalDistinctSeries(t *testing.T) {
	for _, dimension := range []string{"container labels", "disk", "operation"} {
		t.Run(dimension, func(t *testing.T) {
			first := &pod.Container{
				ID: "first", Name: "agent", Hostname: "node",
				Type: pod.ContainerTypeDaemonSet, Qos: pod.ContainerQosLevelMin,
				Labels: map[string]any{"HostNamespace": "namespace"},
			}
			second := *first
			second.ID = "second"
			device, operation := "sda", "read"
			firstKey := ioLatencyContainerKey{Blkcg: 1, Serial: 11, Major: 8}
			secondKey := ioLatencyContainerKey{Blkcg: 2, Serial: 22, Major: 8}
			switch dimension {
			case "container labels":
				second.Name = "other-agent"
			case "disk":
				device = "sdb"
				secondKey.Minor = 16
			case "operation":
				operation = "write"
				secondKey.Operation = 1
			}
			previous := &ioLatencySnapshot{containers: map[ioLatencyContainerKey]*ioLatencyCounters{
				firstKey: {}, secondKey: {},
			}}
			var counters ioLatencyCounters
			counters.Buckets[ioLatencyStageQ2D][0] = 1
			current := &ioLatencySnapshot{
				containers: map[ioLatencyContainerKey]*ioLatencyCounters{
					firstKey: &counters, secondKey: &counters,
				},
			}
			want := appendIOLatencySeries(nil, first, "sda", "read", &counters)
			want = appendIOLatencySeries(want, &second, device, operation, &counters)
			got := appendIOLatencyIntervalMetrics(nil, previous, current,
				map[[2]uint32]string{{8, 0}: "sda", {8, 16}: "sdb"},
				map[uint64]*pod.Container{1: first, 2: &second})
			require.ElementsMatch(t, want, got)
		})
	}
}

// Snapshot decoding and map layout contracts.

func TestIOLatencySnapshotParsing(t *testing.T) {
	readKey := ioLatencyHostKey{Major: 8, Minor: 16}
	writeKey := ioLatencyHostKey{Major: 259, Operation: 1}
	containerKey := ioLatencyContainerKey{Blkcg: 100, Serial: 11, Major: 259, Operation: 1}
	containerReadKey := ioLatencyContainerKey{Blkcg: 100, Serial: 11, Major: 8}
	unknownKey := ioLatencyContainerKey{Blkcg: 200, Serial: 22, Major: 8}
	var readCounters, writeCounters ioLatencyCounters
	readCounters.Buckets[ioLatencyStageQ2D][0] = 2
	readCounters.QueuedSize[0] = 4
	readCounters.IssuedSize[2] = 6
	writeCounters.Buckets[ioLatencyStageQ2G][1] = 3
	writeCounters.QueuedSize[1] = 5
	writeCounters.IssuedSize[3] = 7
	object := &fakeIOLatencyMapBPF{maps: map[string][]bpf.MapItem{
		blkDiskBucketMap: {
			ioControlTestMapItem(uint64(0x1000), ioLatencyDiskCounters{
				Major: 8, Minor: 16, Counters: [2]ioLatencyCounters{readCounters, writeCounters},
			}),
			ioControlTestMapItem(uint64(0x2000), ioLatencyDiskCounters{
				Major: 259, Counters: [2]ioLatencyCounters{1: writeCounters},
			}),
		},
		blkContainerBucketMap: {
			ioControlTestMapItem(containerKey, writeCounters),
			ioControlTestMapItem(containerReadKey, readCounters),
			ioControlTestMapItem(unknownKey, readCounters),
		},
	}}
	session := &ioLatencySession{object: object}
	devices := map[[2]uint32]string{{8, 0}: "sda", {259, 0}: "nvme0n1"}
	host, err := session.captureHostLatency()
	require.NoError(t, err)
	require.Len(t, host, 4)
	require.Equal(t, readCounters, *host[readKey])
	require.Equal(t, writeCounters, *host[ioLatencyHostKey{Major: 8, Minor: 16, Operation: 1}])
	require.Equal(t, writeCounters, *host[writeKey])
	counters, err := session.captureContainerLatency()
	require.NoError(t, err)
	require.Equal(t, map[ioLatencyContainerKey]*ioLatencyCounters{
		containerKey: &writeCounters, containerReadKey: &readCounters, unknownKey: &readCounters,
	}, counters, "raw capture retains identities with unavailable metadata")
	container := &pod.Container{ID: "container-id", Labels: map[string]any{"HostNamespace": "namespace"}}

	t.Run("zero rows and failed reads", func(t *testing.T) {
		rows := object.maps[blkContainerBucketMap]
		t.Cleanup(func() { object.maps[blkContainerBucketMap] = rows; object.errors = nil })
		previous := &ioLatencySnapshot{}
		for _, interval := range []struct {
			total   uint64
			readErr error
			want    uint64
			emit    bool
		}{
			{},
			{total: 3, want: 3, emit: true},
			{total: 5, readErr: unix.EIO},
			{total: 7},
			{total: 10, want: 3, emit: true},
		} {
			var value ioLatencyCounters
			value.Buckets[ioLatencyStageQ2D][0] = interval.total
			object.maps[blkContainerBucketMap] = nil
			if interval.total != 0 {
				object.maps[blkContainerBucketMap] = []bpf.MapItem{ioControlTestMapItem(containerReadKey, value)}
			}
			object.errors = map[string]error{blkContainerBucketMap: interval.readErr}
			samples, err := session.captureContainerLatency()
			require.ErrorIs(t, err, interval.readErr)
			current := &ioLatencySnapshot{
				containers: samples,
			}
			metrics := appendIOLatencyIntervalMetrics(nil, previous, current,
				devices, map[uint64]*pod.Container{100: container})
			if interval.emit {
				value.Buckets[ioLatencyStageQ2D][0] = interval.want
				require.ElementsMatch(t, appendIOLatencySeries(nil, container, "sda", "read", &value), metrics)
			} else {
				require.Empty(t, metrics)
			}
			previous = current
		}
	})
	t.Run("invalid labels retain raw baselines", func(t *testing.T) {
		previous := &ioLatencySnapshot{containers: counters}
		for _, interval := range []struct {
			label any
			total uint64
			want  uint64
			emit  bool
		}{
			{label: 123, total: 8},
			{label: "namespace", total: 13, want: 5, emit: true},
			{label: "namespace", total: 15, want: 2, emit: true},
		} {
			container.Labels["HostNamespace"] = interval.label
			writeCounters.Buckets[ioLatencyStageQ2G][1] = interval.total
			object.maps[blkContainerBucketMap][0] = ioControlTestMapItem(containerKey, writeCounters)
			samples, err := session.captureContainerLatency()
			require.NoError(t, err)
			require.Len(t, samples, 3)
			current := &ioLatencySnapshot{
				containers: samples,
			}
			got := appendIOLatencyIntervalMetrics(nil, previous, current,
				devices, map[uint64]*pod.Container{100: container})
			if interval.emit {
				var readDelta, writeDelta ioLatencyCounters
				writeDelta.Buckets[ioLatencyStageQ2G][1] = interval.want
				want := appendIOLatencySeries(nil, container, "sda", "read", &readDelta)
				want = appendIOLatencySeries(want, container, "nvme0n1", "write", &writeDelta)
				require.ElementsMatch(t, want, got)
			} else {
				require.Empty(t, got)
			}
			previous = current
		}
	})
}

func TestIOLatencyCgroupReleaseCleanup(t *testing.T) {
	for _, failure := range []string{"none", "rescan", "counter delete", "marker delete", "concurrent delete"} {
		t.Run(failure, func(t *testing.T) {
			oldKey := ioLatencyContainerKey{Blkcg: 0x11, Serial: 111, Major: 8}
			secondOldKey := oldKey
			secondOldKey.Operation = 1
			reusedKey := oldKey
			reusedKey.Serial++
			otherKey := ioLatencyContainerKey{Blkcg: 0x22, Serial: 222, Major: 8}
			markerKey := ioLatencyContainerKey{Blkcg: oldKey.Blkcg, Serial: oldKey.Serial, Kind: 1}
			var oldCounters, newCounters ioLatencyCounters
			oldCounters.Buckets[ioLatencyStageQ2D][0] = 9
			newCounters.Buckets[ioLatencyStageQ2D][0] = 1
			oldRow := ioControlTestMapItem(oldKey, oldCounters)
			secondOldRow := ioControlTestMapItem(secondOldKey, oldCounters)
			newRow := ioControlTestMapItem(reusedKey, newCounters)
			otherRow := ioControlTestMapItem(otherKey, newCounters)
			marker := ioControlTestMapItem(markerKey, ioLatencyCounters{})
			object := &fakeIOLatencyContainerBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
				maps: map[string][]bpf.MapItem{
					blkContainerBucketMap: {oldRow, secondOldRow, newRow, otherRow, marker},
				},
				// Release can appear after this traversal has passed its old rows.
				dumps: []ioLatencyTestDump{{items: []bpf.MapItem{newRow, otherRow, marker}}},
			}}
			if failure == "rescan" {
				object.dumps = append(object.dumps, ioLatencyTestDump{err: unix.EIO})
			}
			failed := false
			object.beforeDelete = func(_ uint32, keys [][]byte) error {
				var key ioLatencyContainerKey
				require.NoError(t, decodeBPFMapData(keys[0], &key))
				if key.Kind == 1 {
					for _, row := range object.maps[blkContainerBucketMap] {
						var existing ioLatencyContainerKey
						require.NoError(t, decodeBPFMapData(row.Key, &existing))
						if existing.Kind == 0 && existing.Blkcg == oldKey.Blkcg && existing.Serial == oldKey.Serial {
							t.Fatal("release marker was deleted while old counters remained")
						}
					}
				}
				if failed {
					return nil
				}
				if (failure == "counter delete" && key.Kind == 0) ||
					(failure == "marker delete" && key.Kind == 1) {
					failed = true
					return unix.EIO
				}
				if failure == "concurrent delete" && key.Kind == 0 {
					failed = true
					object.maps[blkContainerBucketMap] = slices.DeleteFunc(object.maps[blkContainerBucketMap], func(row bpf.MapItem) bool {
						return bytes.Equal(row.Key, keys[0])
					})
					return unix.ENOENT
				}
				return nil
			}
			session := &ioLatencySession{object: object}
			got, err := session.captureContainerLatency()
			require.NoError(t, err)
			require.Equal(t, map[ioLatencyContainerKey]*ioLatencyCounters{
				reusedKey: &newCounters, otherKey: &newCounters,
			}, got, "a released serial must never be re-exported")
			if failure != "none" {
				require.Contains(t, object.maps[blkContainerBucketMap], marker)
			}
			got, err = session.captureContainerLatency()
			require.NoError(t, err)
			require.Equal(t, map[ioLatencyContainerKey]*ioLatencyCounters{
				reusedKey: &newCounters, otherKey: &newCounters,
			}, got)
			require.ElementsMatch(t, []bpf.MapItem{newRow, otherRow}, object.maps[blkContainerBucketMap])
		})
	}
}

func TestIOLatencyCgroupSerialBaselines(t *testing.T) {
	container := newIOControlAttributionTestContainer("container", 0x11, time.Unix(100, 0))
	oldKey := ioLatencyContainerKey{Blkcg: 0x11, Serial: 111, Major: 8}
	newKey := oldKey
	newKey.Serial++
	var oldCounters, newCounters ioLatencyCounters
	oldCounters.Buckets[ioLatencyStageQ2D][0] = 9
	newCounters.Buckets[ioLatencyStageQ2D][0] = 1
	previous := &ioLatencySnapshot{containers: map[ioLatencyContainerKey]*ioLatencyCounters{oldKey: &oldCounters}}
	current := &ioLatencySnapshot{
		containers: map[ioLatencyContainerKey]*ioLatencyCounters{newKey: &newCounters},
	}
	got := appendIOLatencyIntervalMetrics(nil, previous, current,
		ioControlTestDiskNames(), map[uint64]*pod.Container{0x11: container})
	require.ElementsMatch(t, appendIOLatencySeries(nil, container, "sda", "read", &newCounters), got)
}

func TestIOLatencyMapABI(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  any
		length int
	}{
		{name: "queue probe", value: ioLatencyQueueProbe{}, length: 24},
		{name: "queue probe key", value: ioLatencyQueueKey{}, length: 32},
		{name: "host key", value: uint64(0), length: 8},
		{name: "disk counters", value: ioLatencyDiskCounters{}, length: 1024},
		{name: "container key", value: ioLatencyContainerKey{}, length: 32},
		{name: "counters", value: ioLatencyCounters{}, length: 504},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := binary.Size(test.value); got != test.length {
				t.Fatalf("binary size = %d, want %d", got, test.length)
			}
		})
	}

	for _, length := range []int{23, 25} {
		if err := decodeBPFMapData(
			make([]byte, length), &ioLatencyQueueProbe{},
		); err == nil {
			t.Fatalf("map value with length %d was accepted", length)
		}
	}
}

// Host baselines across optional-source and cleanup failures.

// A newly started session measures from its first successful Host baseline.
func TestIOLatencyCollectionHostBaseline(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })

	for _, hasOptionalReadError := range []bool{false, true} {
		t.Run(fmt.Sprintf("optional read error=%t", hasOptionalReadError), func(t *testing.T) {
			object := &fakeIOLatencyMapBPF{maps: map[string][]bpf.MapItem{
				blkDiskBucketMap: {ioControlTestMapItem(uint64(0x1000), ioLatencyDiskCounters{Major: 4095, Minor: 1})},
			}}
			session := &ioLatencySession{
				object: object, previous: &ioLatencySnapshot{}, devices: ioControlTestDiskNames(),
				cancel: func(error) { t.Fatal("optional read canceled host tracing") },
			}
			collector := &iolatencyTracing{session: session}
			metrics, err := collector.Update()
			if err != nil || len(metrics) != 0 || session.previous == nil {
				t.Fatalf("initial collection: metrics %d, baseline %v, error %v",
					len(metrics), session.previous, err)
			}

			key := ioLatencyHostKey{Major: 4095, Minor: 1, Operation: 1}
			for index, interval := range []struct {
				total uint64
				want  float64
			}{
				{total: 100, want: 100},
				{total: 100, want: 0},
				{total: 103, want: 3},
			} {
				if hasOptionalReadError && index == 0 {
					object.errors = map[string]error{blkContainerBucketMap: unix.EIO}
				} else {
					object.errors = nil
				}
				counters := ioLatencyCounters{}
				counters.Buckets[ioLatencyStageQ2D][0] = interval.total
				object.maps[blkDiskBucketMap] = []bpf.MapItem{
					ioControlTestMapItem(uint64(0x1000), ioLatencyDiskCounters{
						Major: key.Major, Minor: key.Minor,
						Counters: [2]ioLatencyCounters{1: counters},
					}),
				}
				metrics, err = collector.Update()
				if err != nil {
					t.Fatalf("collection at total %d: %v", interval.total, err)
				}
				require.Equal(t, interval.total, session.previous.host[key].Buckets[ioLatencyStageQ2D][0])
				seriesSize := ioLatencyStageCount*ioLatencyBucketCount + 2*ioSizeBucketCount
				if len(metrics) != 2*seriesSize+2 {
					t.Fatalf("collection at total %d returned %d metrics",
						interval.total, len(metrics))
				}
				for bucket := range ioLatencyBucketCount {
					want := metric.NewGaugeData(ioLatencyStageMetrics[0].name,
						interval.want, ioLatencyStageMetrics[0].help, map[string]string{
							"disk": "sda", "operation": "write", "le": ioLatencyBucketLabels[bucket],
						})
					if !slices.ContainsFunc(metrics, func(data *metric.Data) bool {
						return reflect.DeepEqual(data, want)
					}) {
						t.Fatalf("total %d, write Q2D bucket %d: missing value %v",
							interval.total, bucket, interval.want)
					}
				}
			}
		})
	}
}

func TestIOLatencyCollectionRebuildsBaselineAfterReadFailure(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })

	for _, source := range []string{blkDiskBucketMap, ioControlStatusMap} {
		t.Run(source, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			failure := errors.New("required map failed")
			key := ioLatencyHostKey{Major: 4095, Minor: 1, Operation: 0}
			previousCounters := ioLatencyCounters{}
			previousCounters.Buckets[ioLatencyStageQ2D][0] = 10
			currentCounters := previousCounters
			currentCounters.Buckets[ioLatencyStageQ2D][0] = 15
			previous := &ioLatencySnapshot{
				capturedAt: time.Unix(100, 0),
				host: map[ioLatencyHostKey]*ioLatencyCounters{
					key: &previousCounters,
				},
				containers: make(map[ioLatencyContainerKey]*ioLatencyCounters),
			}
			object := &fakeIOLatencyMapBPF{
				maps: map[string][]bpf.MapItem{
					blkDiskBucketMap: {
						ioControlTestMapItem(uint64(0x1000), ioLatencyDiskCounters{
							Major: key.Major, Minor: key.Minor,
							Counters: [2]ioLatencyCounters{0: currentCounters},
						}),
					},
				},
				errors: map[string]error{
					source: failure,
				},
			}
			session := &ioLatencySession{
				object:   object,
				previous: previous,
				devices:  ioControlTestDiskNames(),
				cancel:   cancel,
			}
			collector := &iolatencyTracing{session: session}

			for range 2 {
				metrics, err := collector.Update()
				require.ErrorIs(t, err, failure)
				require.Empty(t, metrics)
				require.Nil(t, context.Cause(ctx))
				if session.previous != nil {
					t.Fatal("failed collection retained a valid interval baseline")
				}
				if got := *previous.host[key]; got != previousCounters {
					t.Fatal("failed collection modified the previous snapshot")
				}
			}

			delete(object.errors, source)
			metrics, err := collector.Update()
			if !errors.Is(err, metric.ErrNoData) || len(metrics) != 0 {
				t.Fatalf("recovery Update() = %v, %v; want no-data baseline", metrics, err)
			}
			if *session.previous.host[key] != currentCounters {
				t.Fatal("recovery did not commit the current counters")
			}
			recoveredAt := session.previous.capturedAt
			currentCounters.Buckets[ioLatencyStageQ2D][0] = 20
			object.maps[blkDiskBucketMap] = []bpf.MapItem{
				ioControlTestMapItem(uint64(0x1000), ioLatencyDiskCounters{
					Major: key.Major, Minor: key.Minor,
					Counters: [2]ioLatencyCounters{0: currentCounters},
				}),
			}
			metrics, err = collector.Update()
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			seriesSize := ioLatencyStageCount*ioLatencyBucketCount + 2*ioSizeBucketCount
			wantMetricCount := 2*seriesSize + 2
			if len(metrics) != wantMetricCount {
				t.Fatalf("metric count = %d, want %d", len(metrics),
					wantMetricCount)
			}
			for _, bucket := range []int{0, ioLatencyBucketCount - 1} {
				want := metric.NewGaugeData(ioLatencyStageMetrics[0].name, 5,
					ioLatencyStageMetrics[0].help, map[string]string{
						"disk": "sda", "operation": "read", "le": ioLatencyBucketLabels[bucket],
					})
				if !slices.ContainsFunc(metrics, func(data *metric.Data) bool {
					return reflect.DeepEqual(data, want)
				}) {
					t.Fatalf("read Q2D bucket %d: missing value 5", bucket)
				}
			}
			if metrics[len(metrics)-2].Value != float64(recoveredAt.UnixNano())/1e9 {
				t.Fatalf("interval start = %v, want recovered boundary %v",
					metrics[len(metrics)-2].Value, recoveredAt)
			}
			if session.previous == previous {
				t.Fatal("successful collection did not advance the baseline")
			}

			replacement := &ioLatencySession{
				object: object, previous: &ioLatencySnapshot{}, devices: ioControlTestDiskNames(),
			}
			collector.session = replacement
			metrics, err = collector.Update()
			if err != nil {
				t.Fatalf("replacement Update() error = %v", err)
			}
			if len(metrics) != 0 {
				t.Fatalf("new session exported histogram metrics before a baseline: %v", metrics)
			}
			if replacement.previous == nil {
				t.Fatal("new session did not establish its own baseline")
			}
		})
	}
}

func assertIOLatencyValues(
	t *testing.T,
	metrics []*metric.Data,
	want []float64,
) {
	t.Helper()
	if len(metrics) != len(want) {
		t.Fatalf("metric count = %d, want %d", len(metrics), len(want))
	}
	for index := range want {
		if metrics[index].Value != want[index] {
			t.Fatalf("bucket %d = %v, want %v", index,
				metrics[index].Value, want[index])
		}
	}
}

// Session withdrawal waits for in-flight collection.

type blockingIOLatencyBPF struct {
	bpf.BPF
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingIOLatencyBPF) MapIDByName(string) uint32 {
	return 2
}

func (*blockingIOLatencyBPF) DumpMap(uint32) ([]bpf.MapItem, error) {
	return nil, nil
}

func (*blockingIOLatencyBPF) ReadMap(_ uint32, _ []byte) ([]byte, error) {
	return bytesutil.ToBytes(ioControlStatus{}), nil
}

func (b *blockingIOLatencyBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name != blkDiskBucketMap {
		return nil, nil
	}
	b.once.Do(func() { close(b.started) })
	<-b.release
	return []bpf.MapItem{ioControlTestMapItem(uint64(0x1000), ioLatencyDiskCounters{Major: 8})}, nil
}

func TestIOLatencySessionWaitsForCollectionBeforeWithdrawal(t *testing.T) {
	object := &blockingIOLatencyBPF{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	releaseRead := sync.OnceFunc(func() { close(object.release) })
	t.Cleanup(releaseRead)
	session := &ioLatencySession{object: object, previous: &ioLatencySnapshot{}, devices: ioControlTestDiskNames()}
	collector := &iolatencyTracing{session: session}

	updateDone := make(chan error, 1)
	go func() {
		_, err := collector.Update()
		updateDone <- err
	}()
	select {
	case <-object.started:
	case <-time.After(time.Second):
		t.Fatal("collection did not start its map read")
	}
	withdrawStarted := make(chan struct{})
	withdrawDone := make(chan struct{})
	go func() {
		close(withdrawStarted)
		collector.withdrawSession()
		close(withdrawDone)
	}()
	<-withdrawStarted

	// Withdrawal must wait while the collector is still using the BPF maps.
	select {
	case <-withdrawDone:
		t.Fatal("session was withdrawn while collection used its BPF maps")
	case <-time.After(20 * time.Millisecond):
	}

	releaseRead()
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("Update() returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("collection did not finish after its map read resumed")
	}
	select {
	case <-withdrawDone:
	case <-time.After(time.Second):
		t.Fatal("session withdrawal did not resume after collection")
	}
	if collector.session != nil {
		t.Fatal("withdrawn session remains published")
	}
}

func TestIOLatencyDiskEnrollmentFailure(t *testing.T) {
	for _, failure := range []string{"sysfs", "write", "read", "disarm"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "block", "sda", "queue")
			require.NoError(t, os.MkdirAll(path, 0o700))
			object := &fakeIOLatencyQueueBPF{result: 2}
			if failure != "sysfs" {
				require.NoError(t, os.WriteFile(filepath.Join(path, "iostats"), []byte("1\n"), 0o600))
			}
			switch failure {
			case "write":
				object.writeErr = unix.EIO
			case "read":
				object.readErr = unix.EIO
			case "disarm":
				object.deleteErr = unix.EIO
			}
			session := &ioLatencySession{object: object}
			err := session.discoverDisks(root)
			require.Error(t, err)
			if failure != "sysfs" {
				require.ErrorIs(t, err, unix.EIO)
			}
			if failure != "disarm" {
				require.False(t, object.resultArmed)
			}
		})
	}
}
