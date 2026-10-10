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

// IOLatency tests keep statistics, compatibility, lifecycle, GC, and qualification together.
package collector

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/internal/utils/netutil"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/btf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Statistics, attribution, and collection lifecycle.

// Map-read and container cleanup fixtures.

type fakeIOLatencyMapBPF struct {
	bpf.BPF
	maps       map[string][]bpf.MapItem
	errors     map[string]error
	statusHook func()
	dumpCalls  map[string]int
}

type fakeIOLatencyContainerBPF struct {
	fakeIOLatencyMapBPF
	deleteErr    error
	beforeDelete func(uint32, [][]byte) error
	beforeWrite  func()
}

func (b *fakeIOLatencyContainerBPF) MapIDByName(name string) uint32 {
	if name == blkContainerLatencyMap {
		return 1
	}
	return 2
}

func (b *fakeIOLatencyContainerBPF) DeleteMapItems(id uint32, keys [][]byte) error {
	if b.beforeDelete != nil {
		if err := b.beforeDelete(id, keys); err != nil {
			return err
		}
	}
	if b.deleteErr != nil {
		return b.deleteErr
	}
	name := blkContainerLatencyMap
	if id == 2 {
		name = blkContainerBucketMap
	}
	for _, key := range keys {
		index := slices.IndexFunc(b.maps[name], func(item bpf.MapItem) bool {
			return bytes.Equal(item.Key, key)
		})
		if index < 0 {
			return unix.ENOENT
		}
		b.maps[name] = slices.Delete(b.maps[name], index, index+1)
	}
	return nil
}

func (b *fakeIOLatencyContainerBPF) WriteMapItems(id uint32, items []bpf.MapItem) error {
	if id != 1 {
		return unix.EINVAL
	}
	if b.beforeWrite != nil {
		b.beforeWrite()
	}
	for _, item := range items {
		index := slices.IndexFunc(b.maps[blkContainerLatencyMap], func(existing bpf.MapItem) bool {
			return bytes.Equal(existing.Key, item.Key)
		})
		if index < 0 {
			b.maps[blkContainerLatencyMap] = append(b.maps[blkContainerLatencyMap], item)
		} else {
			b.maps[blkContainerLatencyMap][index] = item
		}
	}
	return nil
}

// Container retirement and concurrent map deletion.

func TestIOLatencyContainerCleanupConcurrentDelete(t *testing.T) {
	for _, test := range []struct {
		name        string
		deleteError error
		removed     bool
		readError   error
	}{
		{name: "missing errno", deleteError: unix.ENOENT, removed: true},
		{name: "remote missing row", deleteError: errors.New("remote delete rejected"), removed: true},
		{name: "row remains", deleteError: unix.EACCES},
		{name: "transport missing socket", deleteError: unix.ENOENT},
		{name: "confirmation fails", deleteError: unix.EIO, removed: true, readError: unix.EAGAIN},
	} {
		t.Run(test.name, func(t *testing.T) {
			const css = uint64(123)
			first := ioLatencyMapItem(ioLatencyContainerKey{Blkcg: css, Major: 8}, ioLatencyCounters{})
			second := ioLatencyMapItem(ioLatencyContainerKey{Blkcg: css, Major: 8, Operation: 1}, ioLatencyCounters{})
			other := ioLatencyMapItem(ioLatencyContainerKey{Blkcg: css + 1, Major: 8}, ioLatencyCounters{})
			object := &fakeIOLatencyContainerBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
				maps: map[string][]bpf.MapItem{
					blkContainerLatencyMap: {ioLatencyMapItem(css, BlkgqEntry{})},
					blkContainerBucketMap:  {first, second, other},
				},
				errors: make(map[string]error),
			}}
			object.beforeDelete = func(id uint32, keys [][]byte) error {
				if id == 1 {
					return nil
				}
				if !bytes.Equal(keys[0], first.Key) {
					return nil
				}
				// BPF revocation recheck removes this row after the userspace dump.
				if test.removed {
					object.maps[blkContainerBucketMap] = []bpf.MapItem{second, other}
				}
				object.errors[blkContainerBucketMap] = test.readError
				return test.deleteError
			}
			session := &ioLatencySession{object: object}
			err := session.deleteContainerLatency([][]byte{bytesutil.ToBytes(css)})
			if !test.removed || test.readError != nil {
				if !errors.Is(err, test.deleteError) {
					t.Fatalf("unconfirmed deletion error = %v, want %v", err, test.deleteError)
				}
				return
			}
			if err != nil {
				t.Fatalf("concurrent deletion requested a session restart: %v", err)
			}
			if got := object.maps[blkContainerBucketMap]; !reflect.DeepEqual(got, []bpf.MapItem{other}) {
				t.Fatalf("cleanup stopped before the remaining key or removed another container: %v", got)
			}
		})
	}
}

// A batch can stop after removing a prefix. Confirm the remaining keys as a
// group, including a row removed concurrently with the individual retry.
func TestIOLatencyContainerCleanupPartialBatch(t *testing.T) {
	for _, retry := range []string{"success", "concurrent removal", "failure"} {
		t.Run(retry, func(t *testing.T) {
			const css = uint64(123)
			var rows []bpf.MapItem
			for minor := uint32(0); minor < 5; minor++ {
				rows = append(rows, ioLatencyMapItem(ioLatencyContainerKey{Blkcg: css, Major: 8, Minor: minor}, ioLatencyCounters{}))
			}
			other := ioLatencyMapItem(ioLatencyContainerKey{Blkcg: css + 1, Major: 8}, ioLatencyCounters{})
			object := &fakeIOLatencyContainerBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
				maps: map[string][]bpf.MapItem{
					blkContainerLatencyMap: {ioLatencyMapItem(css, BlkgqEntry{})},
					blkContainerBucketMap:  append(slices.Clone(rows), other),
				},
				dumpCalls: make(map[string]int),
			}}
			object.beforeDelete = func(id uint32, keys [][]byte) error {
				if id != 2 {
					return nil
				}
				if len(keys) > 1 {
					object.maps[blkContainerBucketMap] = slices.DeleteFunc(object.maps[blkContainerBucketMap], func(item bpf.MapItem) bool {
						return bytes.Equal(item.Key, rows[3].Key)
					})
				} else if bytes.Equal(keys[0], rows[4].Key) && retry != "success" {
					if retry == "concurrent removal" {
						object.maps[blkContainerBucketMap] = []bpf.MapItem{other}
					}
					return unix.EACCES
				}
				return nil
			}
			session := &ioLatencySession{object: object}
			err := session.deleteContainerLatency([][]byte{bytesutil.ToBytes(css)})
			want := []bpf.MapItem{other}
			if retry == "failure" {
				if !errors.Is(err, unix.EACCES) {
					t.Fatalf("remaining row error = %v", err)
				}
				want = []bpf.MapItem{rows[4], other}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(object.maps[blkContainerBucketMap], want) {
				t.Fatalf("remaining rows = %v, want %v", object.maps[blkContainerBucketMap], want)
			}
			limit := 2 // Initial snapshot and confirmation after the partial batch.
			if retry != "success" {
				limit++ // Confirm failed individual retries together.
			}
			if got := object.dumpCalls[blkContainerBucketMap]; got > limit {
				t.Fatalf("cleanup used %d complete counter dumps, want at most %d", got, limit)
			}
		})
	}
}

func (b *fakeIOLatencyMapBPF) DumpMapByName(
	name string,
) ([]bpf.MapItem, error) {
	if b.dumpCalls != nil {
		b.dumpCalls[name]++
	}
	if name == "io_latency_status_map" && b.statusHook != nil {
		b.statusHook()
	}
	if err := b.errors[name]; err != nil {
		return nil, err
	}
	if name == "io_latency_status_map" && b.maps[name] == nil {
		return []bpf.MapItem{
			ioLatencyMapItem(uint32(0), uint64(0)),
			ioLatencyMapItem(uint32(1), uint64(0)),
		}, nil
	}
	return b.maps[name], nil
}

// Collection health, histogram deltas, and rollback boundaries.

// Health is checked on both sides of the map capture. A terminal BPF error
// cancels Start without publishing a partial interval; read errors can retry.
func TestIOLatencyCollectionHealth(t *testing.T) {
	t.Run("unstarted", func(t *testing.T) {
		collector := &iolatencyTracing{}
		metrics, err := collector.Update()
		if !errors.Is(err, metric.ErrNoData) || len(metrics) != 0 {
			t.Fatalf("unstarted Update() = %v, %v; want ErrNoData", metrics, err)
		}
	})
	for _, test := range []struct {
		name         string
		status       uint64
		readError    bool
		afterCapture bool
		want         string
	}{
		{name: "request chain exceeds budget", status: 1 << 32, want: "512"},
		{name: "fallback capacity", status: 2<<32 | 0xfffffff9, want: "bio_fallback_map capacity exhausted (10240 entries)"},
		{name: "fallback insertion", status: 2<<32 | 0xfffffff4, want: "bio_fallback_map insertion failed"},
		{name: "fallback deletion", status: 5<<32 | 0xfffffffe, want: "bio_fallback_map"},
		{name: "failure during capture", status: 1 << 32, afterCapture: true, want: "512"},
		{name: "temporary status read", readError: true, want: "status unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			previous := &ioLatencySnapshot{}
			object := &fakeIOLatencyMapBPF{
				maps:   make(map[string][]bpf.MapItem),
				errors: make(map[string]error),
			}
			reads := 0
			object.statusHook = func() {
				reads++
				if test.readError {
					object.errors["io_latency_status_map"] = errors.New("status unavailable")
				} else if !test.afterCapture || reads == 2 {
					object.maps["io_latency_status_map"] = []bpf.MapItem{
						ioLatencyMapItem(uint32(0), test.status),
						ioLatencyMapItem(uint32(1), uint64(0)),
					}
				}
			}
			session := &ioLatencySession{object: object, previous: previous, cancel: cancel}
			collector := &iolatencyTracing{session: session}
			metrics, err := collector.Update()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Update() error = %v, want %q", err, test.want)
			}
			if len(metrics) != 0 || session.previous != previous {
				t.Fatal("failed capture published metrics or advanced the baseline")
			}
			if !test.readError {
				if !errors.Is(err, types.ErrTracingStopped) ||
					!errors.Is(context.Cause(ctx), types.ErrTracingStopped) {
					t.Fatalf("fatal error did not stop tracing: %v, cause %v", err, context.Cause(ctx))
				}
				collector.withdrawSession()
				metrics, err = collector.Update()
				if !errors.Is(err, metric.ErrNoData) || len(metrics) != 0 {
					t.Fatalf("stopped Update() = %v, %v; want ErrNoData", metrics, err)
				}
				return
			}
			if context.Cause(ctx) != nil {
				t.Fatalf("temporary read canceled tracing: %v", context.Cause(ctx))
			}
			object.statusHook = nil
			delete(object.errors, "io_latency_status_map")
			if _, err := collector.Update(); err != nil {
				t.Fatalf("retry did not recover: %v", err)
			}
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
				&ioLatencySnapshot{host: map[ioLatencyHostKey]*ioLatencyHostSample{
					key: {device: "sda", operation: "read", counters: previous},
				}},
				&ioLatencySnapshot{host: map[ioLatencyHostKey]*ioLatencyHostSample{
					key: {device: "sda", operation: "read", counters: current},
				}},
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
				containers: make(map[ioLatencyContainerKey]*ioLatencyContainerSample),
			}
			current := &ioLatencySnapshot{
				containers: make(map[ioLatencyContainerKey]*ioLatencyContainerSample),
			}
			// Distinct raw baselines must be subtracted before public labels
			// are combined, including when one row is idle or rolls back.
			for index, container := range []*pod.Container{first, &second} {
				before, after := uint64(10), uint64(12)
				if index == 1 {
					before, after = 100, test.secondTotal
				}
				key := ioLatencyContainerKey{Blkcg: uint64(index + 1), Major: 8}
				old := &ioLatencyContainerSample{container: container, device: "sda", operation: "read"}
				next := *old
				for stage := range old.counters.Buckets {
					for bucket := range old.counters.Buckets[stage] {
						old.counters.Buckets[stage][bucket] = before
						next.counters.Buckets[stage][bucket] = after
					}
				}
				for bucket := range old.counters.QueuedSize {
					old.counters.QueuedSize[bucket], old.counters.IssuedSize[bucket] = before, before
					next.counters.QueuedSize[bucket], next.counters.IssuedSize[bucket] = after, after
				}
				previous.containers[key], current.containers[key] = old, &next
			}

			got := appendIOLatencyIntervalMetrics(nil, previous, current)
			var want []*metric.Data
			for _, definition := range ioLatencyStageMetrics {
				for bucket, bound := range ioLatencyBucketLabels {
					want = append(want, metric.NewContainerGaugeData(first,
						definition.name, float64(bucket+1)*test.wantPerBucket,
						definition.help, map[string]string{
							"device": "sda", "operation": "read", "le": bound,
						}))
				}
			}
			for _, definition := range []ioLatencyBucketMetric{ioLatencyQueuedSizeMetric, ioLatencyIssuedSizeMetric} {
				for bucket, bound := range ioSizeBucketLabels {
					want = append(want, metric.NewContainerGaugeData(first,
						definition.name, float64(bucket+1)*test.wantPerBucket,
						definition.help, map[string]string{
							"device": "sda", "operation": "read", "le": bound,
						}))
				}
			}
			require.Len(t, got, len(want))
			require.ElementsMatch(t, want, got)
		})
	}
}

func TestIOLatencyContainerIntervalDistinctSeries(t *testing.T) {
	for _, dimension := range []string{"container labels", "device", "operation"} {
		t.Run(dimension, func(t *testing.T) {
			first := &pod.Container{
				ID: "first", Name: "agent", Hostname: "node",
				Type: pod.ContainerTypeDaemonSet, Qos: pod.ContainerQosLevelMin,
				Labels: map[string]any{"HostNamespace": "namespace"},
			}
			second := *first
			second.ID = "second"
			device, operation := "sda", "read"
			switch dimension {
			case "container labels":
				second.Name = "other-agent"
			case "device":
				device = "sdb"
			case "operation":
				operation = "write"
			}
			firstKey := ioLatencyContainerKey{Blkcg: 1, Major: 8}
			secondKey := ioLatencyContainerKey{Blkcg: 2, Major: 8}
			if dimension == "device" {
				secondKey.Minor = 16
			} else if dimension == "operation" {
				secondKey.Operation = 1
			}
			previous := &ioLatencySnapshot{containers: map[ioLatencyContainerKey]*ioLatencyContainerSample{
				firstKey:  {container: first, device: "sda", operation: "read"},
				secondKey: {container: &second, device: device, operation: operation},
			}}
			var counters ioLatencyCounters
			counters.Buckets[ioLatencyStageQ2D][0] = 1
			current := &ioLatencySnapshot{containers: map[ioLatencyContainerKey]*ioLatencyContainerSample{
				firstKey:  {container: first, device: "sda", operation: "read", counters: counters},
				secondKey: {container: &second, device: device, operation: operation, counters: counters},
			}}
			want := appendIOLatencySeries(nil, first, "sda", "read", &counters)
			want = appendIOLatencySeries(want, &second, device, operation, &counters)
			got := appendIOLatencyIntervalMetrics(nil, previous, current)
			require.ElementsMatch(t, want, got)
		})
	}
}

// Snapshot decoding and map layout contracts.

func TestIOLatencySnapshotParsing(t *testing.T) {
	readKey := ioLatencyHostKey{Major: 8, Minor: 0, Operation: 0}
	writeKey := ioLatencyHostKey{Major: 259, Minor: 0, Operation: 1}
	containerKey := ioLatencyContainerKey{
		Blkcg: 100, Major: 259, Minor: 0, Operation: 1,
	}
	containerReadKey := ioLatencyContainerKey{
		Blkcg: 100, Major: 8, Minor: 0, Operation: 0,
	}
	unknownContainerKey := ioLatencyContainerKey{
		Blkcg: 200, Major: 8, Minor: 0, Operation: 0,
	}
	readCounters := ioLatencyCounters{}
	readCounters.Buckets[ioLatencyStageQ2D][0] = 2
	readCounters.QueuedSize[0] = 4
	readCounters.IssuedSize[2] = 6
	writeCounters := ioLatencyCounters{}
	writeCounters.Buckets[ioLatencyStageQ2G][1] = 3
	writeCounters.QueuedSize[1] = 5
	writeCounters.IssuedSize[3] = 7

	object := &fakeIOLatencyMapBPF{maps: map[string][]bpf.MapItem{
		blkDiskBucketMap: {
			ioLatencyMapItem(uint64(0x1000), ioLatencyDiskCounters{
				Major: readKey.Major, Minor: readKey.Minor,
				Counters: [2]ioLatencyCounters{0: readCounters},
			}),
			ioLatencyMapItem(uint64(0x2000), ioLatencyDiskCounters{
				Major: writeKey.Major, Minor: writeKey.Minor,
				Counters: [2]ioLatencyCounters{1: writeCounters},
			}),
		},
		blkContainerBucketMap: {
			ioLatencyMapItem(containerKey, writeCounters),
			ioLatencyMapItem(containerReadKey, readCounters),
			ioLatencyMapItem(unknownContainerKey, readCounters),
		},
	}}
	session := &ioLatencySession{object: object}
	devices := map[[2]uint32]string{
		{8, 0}:   "sda",
		{259, 0}: "nvme0n1",
	}

	host, _, err := session.captureHostLatency(devices)
	if err != nil {
		t.Fatalf("captureHostLatency() error = %v", err)
	}
	if len(host) != 4 {
		t.Fatalf("host sample count = %d, want 4", len(host))
	}
	if sample := host[readKey]; sample.device != "sda" ||
		sample.operation != "read" || sample.counters != readCounters {
		t.Fatalf("read sample = %#v", sample)
	}
	if sample := host[writeKey]; sample.device != "nvme0n1" ||
		sample.operation != "write" || sample.counters != writeCounters {
		t.Fatalf("write sample = %#v", sample)
	}

	container := &pod.Container{
		ID:     "container-id",
		Labels: map[string]any{"HostNamespace": "namespace"},
	}
	containerData, err := session.captureContainerLatency(
		map[uint64]*pod.Container{100: container},
		devices,
	)
	if err != nil {
		t.Fatalf("captureContainerLatency() error = %v", err)
	}
	if len(containerData) != 2 {
		t.Fatalf("container sample count = %d, want 2", len(containerData))
	}
	sample := containerData[containerKey]
	if sample.container != container || sample.device != "nvme0n1" ||
		sample.operation != "write" || sample.counters != writeCounters {
		t.Fatalf("container sample = %#v", sample)
	}
	readSample := containerData[containerReadKey]
	if readSample.container != container || readSample.device != "sda" ||
		readSample.operation != "read" ||
		readSample.counters != readCounters {
		t.Fatalf("container read sample = %#v", readSample)
	}

	t.Run("invalid labels and recovery", func(t *testing.T) {
		previous := &ioLatencySnapshot{host: host, containers: containerData}
		seriesSize := ioLatencyStageCount*ioLatencyBucketCount + 2*ioSizeBucketCount
		// Recovered metadata first establishes a container baseline; only
		// subsequent intervals may publish container deltas.
		for _, interval := range []struct {
			label               any
			total               uint64
			wantSamples         int
			wantContainerSeries int
		}{
			{label: 123, total: 8, wantSamples: 0, wantContainerSeries: 0},
			{label: "namespace", total: 13, wantSamples: 2, wantContainerSeries: 0},
			{label: "namespace", total: 15, wantSamples: 2, wantContainerSeries: 2},
		} {
			container.Labels["HostNamespace"] = interval.label
			writeCounters.Buckets[ioLatencyStageQ2G][1] = interval.total
			object.maps[blkContainerBucketMap][0] = ioLatencyMapItem(containerKey, writeCounters)
			host, _, err := session.captureHostLatency(devices)
			if err != nil {
				t.Fatalf("host capture at total %d: %v", interval.total, err)
			}
			containerData, err := session.captureContainerLatency(
				map[uint64]*pod.Container{100: container}, devices,
			)
			if err != nil || len(containerData) != interval.wantSamples {
				t.Fatalf("container capture at total %d: samples=%d, err=%v",
					interval.total, len(containerData), err)
			}
			current := &ioLatencySnapshot{host: host, containers: containerData}
			metrics := appendIOLatencyIntervalMetrics(nil, previous, current)
			if len(metrics) != (len(host)+interval.wantContainerSeries)*seriesSize {
				t.Fatalf("interval at total %d exported %d metrics", interval.total, len(metrics))
			}
			if interval.wantContainerSeries != 0 {
				want := metric.NewContainerGaugeData(container,
					ioLatencyStageMetrics[ioLatencyStageQ2G].name, 2,
					ioLatencyStageMetrics[ioLatencyStageQ2G].help, map[string]string{
						"device": "nvme0n1", "operation": "write", "le": "+Inf",
					})
				if !slices.ContainsFunc(metrics, func(data *metric.Data) bool {
					return reflect.DeepEqual(data, want)
				}) {
					t.Fatal("container recovery did not export only the new interval")
				}
			}
			previous = current
		}
	})
}

// Freeze totals remain cumulative independently of latency-series admission.
func TestIOLatencyFreezeCounter(t *testing.T) {
	object := &fakeIOLatencyMapBPF{maps: make(map[string][]bpf.MapItem)}
	collector := &iolatencyTracing{session: &ioLatencySession{object: object}}
	for _, total := range []uint64{0, 7, 7, 9} {
		object.maps[blkDiskLatencyMap] = []bpf.MapItem{
			ioLatencyMapItem(uint64(0x1000), BlkDiskEntry{
				Disk: 0x1000, Major: 8, FreezeNr: total,
			}),
		}
		metrics, err := collector.Update()
		require.NoError(t, err)
		index := slices.IndexFunc(metrics, func(data *metric.Data) bool {
			return data.Name() == "blkdisk_freeze"
		})
		require.NotEqual(t, -1, index)
		require.Equal(t, metric.NewCounterData(
			"blkdisk_freeze", float64(total), "the disk freeze event count",
			map[string]string{"disk": "8:0"},
		), metrics[index], "freeze total %d", total)
	}
}

func TestIOLatencyMapABI(t *testing.T) {
	for _, test := range []struct {
		name   string
		value  any
		length int
	}{
		{name: "disk", value: BlkDiskEntry{}, length: 24},
		{name: "blkcg", value: BlkgqEntry{}, length: 8},
		{name: "host key", value: uint64(0), length: 8},
		{name: "disk counters", value: ioLatencyDiskCounters{}, length: 1024},
		{name: "container key", value: ioLatencyContainerKey{}, length: 24},
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
			make([]byte, length), &BlkDiskEntry{},
		); err == nil {
			t.Fatalf("map value with length %d was accepted", length)
		}
	}
}

// Host baselines across optional-source and cleanup failures.

// A Host series created after a successful collection contributes its first
// batch once, then uses the same interval baseline as existing series.
func TestIOLatencyCollectionNewHostSeries(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })

	object := &fakeIOLatencyMapBPF{maps: make(map[string][]bpf.MapItem)}
	session := &ioLatencySession{
		object: object,
	}
	collector := &iolatencyTracing{session: session}
	metrics, err := collector.Update()
	if err != nil || len(metrics) != 0 || session.previous == nil {
		t.Fatalf("initial collection: metrics %d, baseline %v, error %v",
			len(metrics), session.previous, err)
	}

	key := ioLatencyHostKey{Major: 4095, Minor: 1, Operation: 1}
	for _, interval := range []struct {
		total uint64
		want  float64
	}{
		{total: 100, want: 100},
		{total: 100, want: 0},
		{total: 103, want: 3},
	} {
		counters := ioLatencyCounters{}
		counters.Buckets[ioLatencyStageQ2D][0] = interval.total
		object.maps[blkDiskBucketMap] = []bpf.MapItem{
			ioLatencyMapItem(uint64(0x1000), ioLatencyDiskCounters{
				Major: key.Major, Minor: key.Minor,
				Counters: [2]ioLatencyCounters{1: counters},
			}),
		}
		metrics, err = collector.Update()
		if err != nil {
			t.Fatalf("collection at total %d: %v", interval.total, err)
		}
		seriesSize := ioLatencyStageCount*ioLatencyBucketCount + 2*ioSizeBucketCount
		if len(metrics) != 2*seriesSize+2 {
			t.Fatalf("collection at total %d returned %d metrics",
				interval.total, len(metrics))
		}
		for bucket := range ioLatencyBucketCount {
			want := metric.NewGaugeData(ioLatencyStageMetrics[0].name,
				interval.want, ioLatencyStageMetrics[0].help, map[string]string{
					"device": "4095:1", "operation": "write", "le": ioLatencyBucketLabels[bucket],
				})
			if !slices.ContainsFunc(metrics, func(data *metric.Data) bool {
				return reflect.DeepEqual(data, want)
			}) {
				t.Fatalf("total %d, write Q2D bucket %d: missing value %v",
					interval.total, bucket, interval.want)
			}
		}
	}
}

// Isolate pod manager globals while exercising the real cached/strict APIs.
func TestIOLatencyCollectionContainerQueryFailure(t *testing.T) {
	const child = "HUATUO_TEST_IOLATENCY_CONTAINER_FAILURE"
	if os.Getenv(child) == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0],
			"-test.run=^TestIOLatencyCollectionContainerQueryFailure$")
		command.Env = append(os.Environ(), child+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("container query regression: %v\n%s", err, output)
		}
		return
	}
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })

	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case requests <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(pod.ReleaseManager)
	port := uint32(server.Listener.Addr().(*net.TCPAddr).Port)
	// HTTP discovery fails first; the target's HTTPS fallback then consumes
	// this missing client certificate before publishing its source error.
	clientCertificatePath := filepath.Join(t.TempDir(), "missing-client.pem")
	managerConfig := &pod.ManagerCtx{
		PodReadOnlyPort:   port,
		PodClientCertPath: clientCertificatePath,
	}
	waitForProducerFailure := func() {
		t.Helper()
		require.Eventually(t, func() bool {
			_, err := pod.SynchronizedContainers()
			return errors.Is(err, os.ErrNotExist) &&
				strings.Contains(err.Error(), "podlist https: loading client key pair") &&
				strings.Contains(err.Error(), clientCertificatePath)
		}, 2*time.Second, 10*time.Millisecond,
			"producer did not publish the missing-client-certificate error")
	}
	if err := pod.InitManager(managerConfig); err != nil {
		t.Fatalf("initialize container producer: %v", err)
	}
	select {
	case <-requests:
	case <-time.After(3 * time.Second):
		t.Fatal("container producer did not query the failing kubelet")
	}
	waitForProducerFailure()

	const css = uint64(0x1234)
	key := ioLatencyHostKey{Major: 4095, Minor: 1, Operation: 0}
	containerKey := ioLatencyContainerKey{Blkcg: css, Major: key.Major, Minor: key.Minor}
	counters := ioLatencyCounters{}
	counters.Buckets[ioLatencyStageQ2D][0] = 10
	object := &fakeIOLatencyContainerBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
		maps: map[string][]bpf.MapItem{
			blkContainerLatencyMap: {ioLatencyMapItem(css, BlkgqEntry{})},
			blkContainerBucketMap:  {ioLatencyMapItem(containerKey, counters)},
		},
	}}
	startup := &ioLatencySession{object: object}
	if err := startup.updateContainerBlkDisk(pod.SynchronizedContainers); err != nil || startup.latestContainers != nil {
		t.Errorf("initial query failure blocked host tracing: err=%v, containers=%v", err, startup.latestContainers)
	}

	cached := &pod.Container{
		ID:        "old",
		Labels:    map[string]any{"HostNamespace": "namespace"},
		CgroupCss: map[string]uint64{subsystem.SubsystemBlkIO: css},
	}
	previous := &ioLatencySnapshot{
		capturedAt: time.Unix(100, 0),
		host: map[ioLatencyHostKey]*ioLatencyHostSample{
			key: {device: "4095:1", operation: "read", counters: counters},
		},
		containers: map[ioLatencyContainerKey]*ioLatencyContainerSample{
			containerKey: {container: cached, device: "4095:1", operation: "read", counters: counters},
		},
	}
	session := &ioLatencySession{
		object: object, previous: previous,
		latestContainers: map[string]*pod.Container{cached.ID: cached},
		cancel:           func(error) { t.Error("query failure stopped the session") },
	}
	collector := &iolatencyTracing{session: session}
	seriesSize := ioLatencyStageCount*ioLatencyBucketCount + 2*ioSizeBucketCount
	for _, interval := range []struct {
		total uint64
		want  float64
	}{
		{total: 15, want: 5},
		{total: 18, want: 3},
	} {
		counters.Buckets[ioLatencyStageQ2D][0] = interval.total
		object.maps[blkDiskBucketMap] = []bpf.MapItem{
			ioLatencyMapItem(uint64(0x1000), ioLatencyDiskCounters{
				Major: key.Major, Minor: key.Minor,
				Counters: [2]ioLatencyCounters{0: counters},
			}),
		}
		object.maps[blkContainerBucketMap] = []bpf.MapItem{ioLatencyMapItem(containerKey, counters)}
		if err := session.updateContainerBlkDisk(pod.SynchronizedContainers); err != nil {
			t.Errorf("runtime query failure requested a restart: %v", err)
		}
		metrics, err := collector.Update()
		if err != nil || len(metrics) != 2*seriesSize+2 || session.previous == previous {
			t.Fatalf("query failure at total %d: metrics=%d, err=%v, baseline advanced=%t",
				interval.total, len(metrics), err, session.previous != previous)
		}
		if session.latestContainers[cached.ID] != cached ||
			len(object.maps[blkContainerLatencyMap]) != 1 || len(object.maps[blkContainerBucketMap]) != 1 {
			t.Fatal("failed query changed the container mapping or counters")
		}
		if session.previous.host[key].counters != counters || len(session.previous.containers) != 0 {
			t.Fatal("failed query did not advance host counters without container attribution")
		}
		want := metric.NewGaugeData(ioLatencyStageMetrics[0].name,
			interval.want, ioLatencyStageMetrics[0].help, map[string]string{
				"device": "4095:1", "operation": "read", "le": "+Inf",
			})
		if !slices.ContainsFunc(metrics, func(data *metric.Data) bool {
			return reflect.DeepEqual(data, want)
		}) {
			t.Fatalf("total %d: missing host Q2D delta %v", interval.total, interval.want)
		}
		previous = session.previous
	}

	// Cleanup is optional for Host, including failures after admission was
	// removed. Retry the complete transition rather than only its last helper.
	// The target's shared producer owns refresh and CSS readiness. Its tests
	// cover failed-source recovery; disabling it provides an authoritative empty
	// catalog here without requiring live CSS probes in this collector test.
	pod.ReleaseManager()
	if _, err := pod.SynchronizedContainers(); err != nil {
		t.Fatalf("disabled producer did not provide an empty view: %v", err)
	}
	for _, stage := range []string{"admission", "scan", "delete", "confirmation"} {
		t.Run("cleanup retries/"+stage, func(t *testing.T) {
			pod.ReleaseManager()
			other := &pod.Container{ID: "other", CgroupCss: map[string]uint64{subsystem.SubsystemBlkIO: css + 1}}
			otherKey := containerKey
			otherKey.Blkcg++
			object := &fakeIOLatencyContainerBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
				maps: map[string][]bpf.MapItem{
					blkContainerLatencyMap: {
						ioLatencyMapItem(css, BlkgqEntry{}), ioLatencyMapItem(css+1, BlkgqEntry{}),
					},
					blkContainerBucketMap: {
						ioLatencyMapItem(containerKey, counters), ioLatencyMapItem(otherKey, counters),
					},
				},
				errors: make(map[string]error),
			}}
			switch stage {
			case "admission":
				object.deleteErr = unix.EAGAIN
				object.beforeDelete = func(id uint32, keys [][]byte) error {
					if id == 1 && len(keys) > 1 {
						// The batch completed one key before failing.
						object.maps[blkContainerLatencyMap] = slices.DeleteFunc(
							object.maps[blkContainerLatencyMap], func(item bpf.MapItem) bool {
								return bytes.Equal(item.Key, keys[0])
							})
					}
					return nil
				}
			case "scan":
				object.errors[blkContainerBucketMap] = unix.EAGAIN
			case "delete", "confirmation":
				object.beforeDelete = func(id uint32, _ [][]byte) error {
					if id == 1 {
						return nil
					}
					if stage == "confirmation" {
						object.errors[blkContainerBucketMap] = unix.EAGAIN
					}
					return unix.EIO
				}
			}
			session := &ioLatencySession{
				object: object, previous: previous,
				latestContainers: map[string]*pod.Container{cached.ID: cached, other.ID: other},
				cancel:           func(error) { t.Error("container cleanup canceled Host") },
			}
			collector := &iolatencyTracing{session: session}
			for attempt := range 3 {
				if attempt == 2 {
					object.deleteErr, object.beforeDelete = nil, nil
					clear(object.errors)
				}
				if err := session.updateContainerBlkDisk(pod.SynchronizedContainers); err != nil {
					t.Fatalf("attempt %d requested a Host restart: %v", attempt, err)
				}
				if stage != "admission" && len(object.maps[blkContainerLatencyMap]) != 0 {
					t.Fatal("counter cleanup began before admission was removed")
				}
				if attempt == 2 && (len(session.latestContainers) != 0 ||
					len(object.maps[blkContainerLatencyMap]) != 0 || len(object.maps[blkContainerBucketMap]) != 0) {
					t.Fatal("recovered cleanup left stale registrations or counters")
				}
				current := counters
				current.Buckets[ioLatencyStageQ2D][0] += uint64(attempt+1) * 2
				object.maps[blkDiskBucketMap] = []bpf.MapItem{
					ioLatencyMapItem(uint64(0x1000), ioLatencyDiskCounters{
						Major: key.Major, Minor: key.Minor,
						Counters: [2]ioLatencyCounters{0: current},
					}),
				}
				metrics, err := collector.Update()
				if err != nil || collector.session != session {
					t.Fatalf("cleanup interrupted Host collection: %v", err)
				}
				want := metric.NewGaugeData(ioLatencyStageMetrics[0].name,
					2, ioLatencyStageMetrics[0].help, map[string]string{
						"device": "4095:1", "operation": "read", "le": "+Inf",
					})
				if !slices.ContainsFunc(metrics, func(data *metric.Data) bool {
					return reflect.DeepEqual(data, want)
				}) {
					t.Fatalf("cleanup attempt %d reset the Host baseline", attempt)
				}
			}
			if err := pod.InitManager(managerConfig); err != nil {
				t.Fatalf("restart failing container producer: %v", err)
			}
			waitForProducerFailure()
			if err := session.updateContainerBlkDisk(pod.SynchronizedContainers); err != nil {
				t.Fatalf("an initialized empty mapping was treated as startup: %v", err)
			}
		})
	}
}

// A pod can temporarily disappear while a sidecar restarts. Its surviving
// container keeps the same ID, but must regain admission after cleanup.
func TestIOLatencyContainerCleanupReappearingID(t *testing.T) {
	const css = uint64(0x1234)
	container := &pod.Container{ID: "same", CgroupCss: map[string]uint64{subsystem.SubsystemBlkIO: css}}
	row := ioLatencyMapItem(ioLatencyContainerKey{Blkcg: css, Major: 8}, ioLatencyCounters{})
	object := &fakeIOLatencyContainerBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
		maps: map[string][]bpf.MapItem{
			blkContainerLatencyMap: {ioLatencyMapItem(css, BlkgqEntry{})},
			blkContainerBucketMap:  {row},
		},
		errors: map[string]error{blkContainerBucketMap: unix.EAGAIN},
	}}
	previous := &ioLatencySnapshot{}
	session := &ioLatencySession{
		object: object, previous: previous,
		latestContainers: map[string]*pod.Container{container.ID: container},
	}
	catalog := map[string]*pod.Container{}
	query := func() (map[string]*pod.Container, error) { return catalog, nil }
	if err := session.updateContainerBlkDisk(query); err != nil {
		t.Fatal(err)
	}
	if len(object.maps[blkContainerLatencyMap]) != 0 || len(object.maps[blkContainerBucketMap]) != 1 {
		t.Fatal("expected revoked admission with unfinished counter cleanup")
	}
	// The same ID returns before storage recovers. It cannot bypass cleanup.
	catalog = map[string]*pod.Container{container.ID: container}
	if err := session.updateContainerBlkDisk(query); err != nil {
		t.Fatal(err)
	}
	if len(object.maps[blkContainerLatencyMap]) != 0 {
		t.Fatal("reappearing container was admitted before cleanup recovered")
	}
	clear(object.errors)
	object.beforeWrite = func() {
		if len(object.maps[blkContainerBucketMap]) != 0 {
			t.Error("container was readmitted before old counters were removed")
		}
	}
	if err := session.updateContainerBlkDisk(query); err != nil {
		t.Fatal(err)
	}
	if len(object.maps[blkContainerLatencyMap]) != 1 || len(object.maps[blkContainerBucketMap]) != 0 ||
		session.latestContainers[container.ID] != container || session.previous != previous {
		t.Fatal("reappearing container did not regain admission within the same session")
	}
}

func TestIOLatencyOptionalReadsPreserveHost(t *testing.T) {
	for _, failedMap := range []string{blkContainerBucketMap, blkDiskLatencyMap} {
		t.Run(failedMap, func(t *testing.T) {
			key := ioLatencyHostKey{Major: 4095, Minor: 1}
			previous := &ioLatencySnapshot{
				capturedAt: time.Unix(100, 0),
				host: map[ioLatencyHostKey]*ioLatencyHostSample{
					key: {device: "4095:1", operation: "read"},
				},
			}
			row := ioLatencyDiskCounters{Major: key.Major, Minor: key.Minor}
			row.Counters[0].Buckets[ioLatencyStageQ2D][0] = 5
			object := &fakeIOLatencyMapBPF{
				maps: map[string][]bpf.MapItem{
					blkDiskBucketMap: {ioLatencyMapItem(uint64(1), row)},
				},
				errors: map[string]error{failedMap: errors.New("temporary map request failure")},
			}
			session := &ioLatencySession{
				object: object, previous: previous,
				cancel: func(error) { t.Fatal("optional read canceled host tracing") },
			}
			collector := &iolatencyTracing{session: session}
			for _, total := range []uint64{5, 8} {
				row.Counters[0].Buckets[ioLatencyStageQ2D][0] = total
				object.maps[blkDiskBucketMap] = []bpf.MapItem{ioLatencyMapItem(uint64(1), row)}
				metrics, err := collector.Update()
				if err != nil || session.previous == previous {
					t.Fatalf("optional failure blocked host collection: %v", err)
				}
				wantDelta := float64(total - previous.host[key].counters.Buckets[ioLatencyStageQ2D][0])
				want := metric.NewGaugeData(ioLatencyStageMetrics[0].name, wantDelta,
					ioLatencyStageMetrics[0].help, map[string]string{
						"device": "4095:1", "operation": "read", "le": "+Inf",
					})
				if !slices.ContainsFunc(metrics, func(data *metric.Data) bool { return reflect.DeepEqual(data, want) }) {
					t.Fatalf("host interval is missing delta %v", wantDelta)
				}
				previous = session.previous
				delete(object.errors, failedMap)
			}
		})
	}
}

func TestIOLatencyCollectionKeepsBaselineAfterReadFailure(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })

	key := ioLatencyHostKey{Major: 4095, Minor: 1, Operation: 0}
	previousCounters := ioLatencyCounters{}
	previousCounters.Buckets[ioLatencyStageQ2D][0] = 10
	currentCounters := previousCounters
	currentCounters.Buckets[ioLatencyStageQ2D][0] = 15
	previous := &ioLatencySnapshot{
		capturedAt: time.Unix(100, 0),
		host: map[ioLatencyHostKey]*ioLatencyHostSample{
			key: {
				device:    "4095:1",
				operation: "read",
				counters:  previousCounters,
			},
		},
		containers: make(map[ioLatencyContainerKey]*ioLatencyContainerSample),
	}
	object := &fakeIOLatencyMapBPF{
		maps: map[string][]bpf.MapItem{
			blkDiskBucketMap: {
				ioLatencyMapItem(uint64(0x1000), ioLatencyDiskCounters{
					Major: key.Major, Minor: key.Minor,
					Counters: [2]ioLatencyCounters{0: currentCounters},
				}),
			},
		},
		errors: map[string]error{
			blkDiskBucketMap: errors.New("host map failed"),
		},
	}
	session := &ioLatencySession{
		object:   object,
		previous: previous,
	}
	collector := &iolatencyTracing{session: session}

	if _, err := collector.Update(); err == nil {
		t.Fatal("Update() accepted a failed map read")
	}
	if session.previous != previous {
		t.Fatal("failed collection advanced the baseline")
	}
	if got := session.previous.host[key].counters; got != previousCounters {
		t.Fatal("failed collection modified the previous snapshot")
	}

	delete(object.errors, blkDiskBucketMap)
	metrics, err := collector.Update()
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
				"device": "4095:1", "operation": "read", "le": ioLatencyBucketLabels[bucket],
			})
		if !slices.ContainsFunc(metrics, func(data *metric.Data) bool {
			return reflect.DeepEqual(data, want)
		}) {
			t.Fatalf("read Q2D bucket %d: missing value 5", bucket)
		}
	}
	if metrics[len(metrics)-2].Value != 100 {
		t.Fatalf("interval start = %v, want 100",
			metrics[len(metrics)-2].Value)
	}
	if session.previous == previous {
		t.Fatal("successful collection did not advance the baseline")
	}

	replacement := &ioLatencySession{
		object: object,
	}
	collector.session = replacement
	metrics, err = collector.Update()
	if err != nil {
		t.Fatalf("replacement Update() error = %v", err)
	}
	if len(metrics) != 0 {
		t.Fatalf("new session exported %d metrics before a baseline", len(metrics))
	}
	if replacement.previous == nil {
		t.Fatal("new session did not establish its own baseline")
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

func ioLatencyMapItem(key, value any) bpf.MapItem {
	return bpf.MapItem{
		Key:   bytesutil.ToBytes(key),
		Value: bytesutil.ToBytes(value),
	}
}

// Session withdrawal waits for in-flight collection.

type blockingIOLatencyBPF struct {
	bpf.BPF
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingIOLatencyBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name == ioLatencyStatusMap {
		return []bpf.MapItem{
			ioLatencyMapItem(uint32(0), uint64(0)),
			ioLatencyMapItem(uint32(1), uint64(0)),
		}, nil
	}
	if name != blkDiskLatencyMap {
		return nil, nil
	}
	b.once.Do(func() { close(b.started) })
	<-b.release
	return nil, nil
}

func TestIOLatencySessionWaitsForCollectionBeforeWithdrawal(t *testing.T) {
	object := &blockingIOLatencyBPF{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	releaseRead := sync.OnceFunc(func() { close(object.release) })
	t.Cleanup(releaseRead)
	session := &ioLatencySession{object: object}
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

// Compatibility: raw tracepoints and required hooks.

// Tracepoint and state-map ABI fixtures.

func TestIOLatencyStateMapABI(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the test file")
	}
	object := filepath.Join(
		filepath.Dir(file), "..", "..", "bpf", "iolatency_tracing.o",
	)
	spec, err := ebpf.LoadCollectionSpec(object)
	if err != nil {
		t.Fatalf("load iolatency BPF object: %v", err)
	}
	if err := spec.RewriteConstants(map[string]any{
		ioLatencyRootBlkcg: uint64(0xffffffff82003000),
	}); err != nil {
		t.Fatalf("rewrite root blkcg address: %v", err)
	}
	for _, want := range []struct {
		name       string
		mapType    ebpf.MapType
		keySize    uint32
		valueSize  uint32
		maxEntries uint32
		flags      uint32
	}{
		{
			name: "bio_latency_map", mapType: ebpf.Hash,
			keySize: 8, valueSize: 40, maxEntries: ioLatencyBioStates,
			flags: unix.BPF_F_NO_PREALLOC,
		},
		{
			name: "bio_fallback_map", mapType: ebpf.Hash,
			keySize: 8, valueSize: 40, maxEntries: ioLatencyBioStates,
		},
		{
			name: "io_latency_status_map", mapType: ebpf.Array,
			keySize: 4, valueSize: 8, maxEntries: 2,
		},
		{
			name: "blkcg_map", mapType: ebpf.Hash,
			keySize: 8, valueSize: 8, maxEntries: 2048,
		},
		{
			name: "blkdisk_lat_map", mapType: ebpf.Hash,
			keySize: 8, valueSize: 1024, maxEntries: 128,
			flags: unix.BPF_F_NO_PREALLOC,
		},
		{
			name: "blkdisk_map", mapType: ebpf.Hash,
			keySize: 8, valueSize: 24, maxEntries: 128,
			flags: unix.BPF_F_NO_PREALLOC,
		},
		{
			name: ioLatencyDiskEvents, mapType: ebpf.PerfEventArray,
			keySize: 4, valueSize: 4,
		},
		{
			name: "blkcg_lat_map", mapType: ebpf.Hash,
			keySize: 24, valueSize: 504, maxEntries: 2048 * 128 * 2,
			flags: unix.BPF_F_NO_PREALLOC,
		},
	} {
		stateMap, exists := spec.Maps[want.name]
		if !exists {
			t.Fatalf("%s is missing", want.name)
		}
		if stateMap.Type != want.mapType ||
			stateMap.KeySize != want.keySize ||
			stateMap.ValueSize != want.valueSize ||
			stateMap.MaxEntries != want.maxEntries ||
			stateMap.Flags != want.flags {
			t.Fatalf("%s = type %s key %d value %d max %d flags %#x",
				want.name, stateMap.Type, stateMap.KeySize,
				stateMap.ValueSize, stateMap.MaxEntries, stateMap.Flags)
		}
	}
	for _, program := range []string{
		"trace_bio_queue",
		"trace_bio_remap",
		"trace_request_remap",
		"trace_bio_split",
		"trace_request_complete",
		"kprobe_unprep_clone",
	} {
		if _, exists := spec.Programs[program]; !exists {
			t.Fatalf("required lifecycle program %s is missing", program)
		}
	}

	// Raw completion reads its timestamp and settles the completed bio prefix.
	completion := spec.Programs["trace_request_complete"]
	readsClock := false
	mapReferences := make(map[string]bool)
	for _, instruction := range completion.Instructions {
		if instruction.IsBuiltinCall() &&
			instruction.Constant == int64(asm.FnKtimeGetNs) {
			readsClock = true
		}
		if instruction.IsLoadFromMap() {
			mapReferences[instruction.Reference()] = true
		}
	}
	if !readsClock {
		t.Fatal("bio completion does not read its completion time")
	}
	for _, name := range []string{
		"bio_latency_map", "blkdisk_lat_map", "blkcg_lat_map",
	} {
		if !mapReferences[name] {
			t.Fatalf("bio completion does not access %s", name)
		}
	}

	// Maintenance is loaded from its own ELF and shares only the cache map.
	for name, program := range spec.Programs {
		if program.Type == ebpf.SchedCLS || name == "bio_gc_run" {
			t.Fatalf("maintenance program %s is in the tracing object", name)
		}
	}
	gcSpec, err := ebpf.LoadCollectionSpec(filepath.Join(filepath.Dir(object), "iolatency_gc.o"))
	if err != nil {
		t.Fatalf("load bio GC BPF object: %v", err)
	}
	if len(gcSpec.Programs) != 1 || len(gcSpec.Maps) != 1 {
		t.Fatalf("GC object has %d programs and %d maps, want one of each",
			len(gcSpec.Programs), len(gcSpec.Maps))
	}
	program := gcSpec.Programs["bio_gc_run"]
	if program == nil || program.Type != ebpf.SchedCLS {
		t.Fatalf("GC program = %#v", program)
	}
	readsJob := false
	gcMaps := make(map[string]bool)
	for _, instruction := range program.Instructions {
		if instruction.IsBuiltinCall() && instruction.Constant == int64(asm.FnSkbLoadBytes) {
			readsJob = true
		}
		if instruction.IsLoadFromMap() {
			gcMaps[instruction.Reference()] = true
		}
	}
	if !readsJob {
		t.Fatal("GC program does not read its test-run payload")
	}
	if !reflect.DeepEqual(gcMaps, map[string]bool{"bio_latency_map": true}) {
		t.Fatalf("GC map references = %v, want cache", gcMaps)
	}
	for name, want := range map[string]*ebpf.MapSpec{
		"bio_latency_map": spec.Maps["bio_latency_map"],
	} {
		got := gcSpec.Maps[name]
		if got == nil || got.Type != want.Type || got.KeySize != want.KeySize ||
			got.ValueSize != want.ValueSize || got.MaxEntries != want.MaxEntries || got.Flags != want.Flags {
			t.Fatalf("GC map %s = %#v, want ABI %#v", name, got, want)
		}
	}
	if size := binary.Size(ioLatencyBioState{}); size != 40 {
		t.Fatalf("Go bio state size = %d, want 40", size)
	}
	if size := binary.Size(ioLatencyGCJob{}); size != 24 {
		t.Fatalf("Go GC job size = %d, want 24", size)
	}
}

type fakeIOLatencyAttacher struct {
	bpf.BPF
	attached  [][]string
	options   []bpf.AttachOption
	fail      map[string]error
	detached  []string
	detachErr error
}

// Full layout bounds belong to kernel qualification, not startup selection.
var ioLatencyTestLayouts = []struct {
	symbol          string
	constant        string
	arguments       uint32
	legacyArguments uint32
}{
	{"block_rq_complete", "", 3, 3},
	{"block_bio_queue", ioLatencyQueueBioArgument, 1, 2},
	{"block_bio_remap", ioLatencyRemapBioArgument, 3, 4},
	{"block_rq_remap", ioLatencyRemapRequestArgument, 3, 4},
	{"block_split", ioLatencySplitBioArgument, 2, 3},
}

func (b *fakeIOLatencyAttacher) AttachWithOptions(
	options []bpf.AttachOption,
) error {
	b.options = append(b.options, options...)
	programs := make([]string, 0, len(options))
	for _, option := range options {
		programs = append(programs, option.ProgramName)
	}
	b.attached = append(b.attached, programs)
	for _, program := range programs {
		if err := b.fail[program]; err != nil {
			return err
		}
	}
	return nil
}

func (b *fakeIOLatencyAttacher) DetachProgram(program string) error {
	b.detached = append(b.detached, program)
	return b.detachErr
}

// Argument probes distinguish compatibility boundaries from errors.

func TestIOLatencyArgumentProbeObject(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the test file")
	}
	spec, err := ebpf.LoadCollectionSpec(filepath.Join(
		filepath.Dir(file), "..", "..", "bpf", "iolatency_probe.o"))
	if err != nil {
		t.Fatal(err)
	}
	var lastSlot uint32
	for _, definition := range ioLatencyTestLayouts {
		lastSlot = max(lastSlot, definition.legacyArguments)
	}
	if len(spec.Maps) != 0 || len(spec.Programs) != int(lastSlot)+1 {
		t.Fatalf("probe object has %d maps and %d programs", len(spec.Maps), len(spec.Programs))
	}
	// The kernel must see one fixed context load. The remaining instructions
	// return zero, so running a probe has no event-side effects.
	for slot := uint32(0); slot <= lastSlot; slot++ {
		program := spec.Programs[fmt.Sprintf("probe_arg%d", slot)]
		if program == nil || program.Type != ebpf.RawTracepoint ||
			program.SectionName != "raw_tracepoint/block_bio_queue" {
			t.Fatalf("missing raw argument probe %d", slot)
		}
		insns := program.Instructions
		if len(insns) != 3 ||
			insns[0].OpCode != asm.LoadMem(asm.R0, asm.R1, 0, asm.DWord).OpCode ||
			insns[0].Src != asm.R1 || insns[0].Offset != int16(slot*8) ||
			insns[1].OpCode != asm.Mov.Imm(asm.R0, 0).OpCode ||
			insns[1].Dst != asm.R0 || insns[1].Constant != 0 ||
			insns[2].OpCode != asm.Return().OpCode {
			t.Fatalf("argument %d probe instructions: %v", slot, insns)
		}
	}
}

func TestIOLatencyArgumentProbeErrorsAndCleanup(t *testing.T) {
	for _, test := range []struct {
		name      string
		attachErr error
		detachErr error
		available bool
		wantErr   error
	}{
		{name: "valid slot", available: true},
		{name: "out of bounds", attachErr: fmt.Errorf("raw attach: %w", unix.EINVAL)},
		{name: "permission", attachErr: unix.EPERM, wantErr: unix.EPERM},
		{name: "resources", attachErr: unix.ENOMEM, wantErr: unix.ENOMEM},
		{name: "missing event", attachErr: unix.ENOENT, wantErr: unix.ENOENT},
		{name: "detach failure", detachErr: unix.EINVAL, wantErr: unix.EINVAL},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := &fakeIOLatencyAttacher{
				fail:      map[string]error{"probe_arg1": test.attachErr},
				detachErr: test.detachErr,
			}
			available, err := probeIOLatencyRawArgument(object, "block_bio_queue", 1)
			if available != test.available || !errors.Is(err, test.wantErr) {
				t.Fatalf("probe = (%t, %v), want (%t, %v)", available, err, test.available, test.wantErr)
			}
			if test.attachErr == nil && !slices.Equal(object.detached, []string{"probe_arg1"}) {
				t.Fatalf("attached probe was not released: %v", object.detached)
			}
		})
	}
}

// Opt-in kernel argument qualification.

// TEST_INTEGRATION=true exercises the active backend against the live kernel.
func TestIOLatencyArgumentProbeKernel(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to probe live block tracepoints")
	}
	if err := bpf.Init(&bpf.Option{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bpf.Shutdown)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the test file")
	}
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join(filepath.Dir(file), "..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = oldDir })
	constants, err := loadIOLatencyTracepointArguments()
	if err != nil {
		t.Fatal(err)
	}
	// Check the complete live ABI independently of the production selector.
	probe, err := bpf.LoadBPF("iolatency_probe.o", nil)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if err := probe.Close(); err != nil {
				t.Error(err)
			}
		}()
		for _, layout := range ioLatencyTestLayouts {
			var count uint32
			for ; count <= layout.legacyArguments; count++ {
				available, err := probeIOLatencyRawArgument(probe, layout.symbol, count)
				if err != nil {
					t.Fatal(err)
				}
				if !available {
					break
				}
			}
			if count != layout.arguments && count != layout.legacyArguments {
				t.Fatalf("%s has unsupported argument count %d", layout.symbol, count)
			}
			var want uint32
			if count != layout.arguments {
				want = 1
			}
			assertIOLatencyConstant(t, constants, layout.constant, want)
		}
	}()
	for _, definition := range ioLatencyTracepoints {
		if definition.constant != "" {
			t.Logf("%s pointer argument: %v", definition.symbol, constants[definition.constant])
		} else {
			t.Logf("%s pointer argument: 0", definition.symbol)
		}
	}
	kernelConstants, statDisable, err := loadIOLatencyKernelConstants()
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range kernelConstants {
		constants[name] = value
	}
	// Load both profiles: constant rewriting changes the verifier's paths.
	var object bpf.BPF
	for _, containers := range []bool{false, true} {
		t.Logf("load with container accounting: %t", containers)
		constants["io_latency_containers_enabled"] = containers
		object, err = bpf.LoadBPF("iolatency_tracing.o", constants)
		if err != nil {
			var verifier *ebpf.VerifierError
			if errors.As(err, &verifier) {
				t.Logf("%-100v", verifier)
			}
			t.Fatal(err)
		}
		if !containers {
			if err := object.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		if err := object.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	session := &ioLatencySession{object: object, diskEvents: make(chan struct{}, 1)}
	reader, err := object.EventPipeByName(ctx, ioLatencyDiskEvents,
		uint32(os.Getpagesize()))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.readDiskEvents(ctx, reader)
	}()
	t.Cleanup(func() {
		cancel()
		reader.Close()
		<-done
	})
	if err := attachIOLatencyHooks(object, statDisable); err != nil {
		t.Fatal(err)
	}
	if err := session.refreshDisks(true); err != nil {
		t.Fatal(err)
	}
	runIOLatencyDiskConfigQualification(t, session)
	runIOLatencyReadQualification(t, object)
}

// Known raw layouts and required hook coverage.

func TestIOLatencyTracepointABI(t *testing.T) {
	// A backport may change any single event independently of its neighbors.
	for _, changed := range ioLatencyTestLayouts {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy-%t", changed.symbol, legacy), func(t *testing.T) {
				counts := make(map[string]uint32)
				for _, definition := range ioLatencyTestLayouts {
					count := definition.arguments
					if legacy != (definition.symbol == changed.symbol) {
						count = definition.legacyArguments
					}
					counts[definition.symbol] = count
				}
				calls := make(map[string]int)
				constants, err := ioLatencyTracepointArguments(
					func(symbol string, argument uint32) (bool, error) {
						calls[symbol]++
						return argument < counts[symbol], nil
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				for _, definition := range ioLatencyTestLayouts {
					wantCalls := 1
					if definition.constant == "" {
						wantCalls = 0
					}
					if calls[definition.symbol] != wantCalls {
						t.Fatalf("%s probe calls = %d, want %d", definition.symbol, calls[definition.symbol], wantCalls)
					}
					var want uint32
					if counts[definition.symbol] != definition.arguments {
						want = 1
					}
					assertIOLatencyConstant(t, constants, definition.constant, want)
				}
			})
		}
	}
}

// Operational failures preserve their error and abort the whole profile;
// none of them may be mistaken for an argument-count boundary.
func TestIOLatencyTracepointProbeErrorsDoNotSelectArguments(t *testing.T) {
	for _, definition := range ioLatencyTracepoints {
		if definition.constant == "" {
			continue
		}
		for _, failure := range []error{
			unix.EPERM, unix.ENOMEM, unix.ENOENT,
		} {
			t.Run(fmt.Sprintf("%s/%v", definition.symbol, failure), func(t *testing.T) {
				constants, err := ioLatencyTracepointArguments(
					func(symbol string, argument uint32) (bool, error) {
						if symbol == definition.symbol {
							return false, failure
						}
						return false, nil
					},
				)
				if !errors.Is(err, failure) || errors.Is(err, types.ErrTracingStopped) ||
					constants != nil || !strings.Contains(err.Error(), definition.symbol) {
					t.Fatalf("failed probe: constants=%v, err=%v", constants, err)
				}
			})
		}
	}
}

func TestAttachIOLatencyHooksRequiresEveryPath(t *testing.T) {
	programs := []string{
		"kprobe_disk_release", "kretprobe_register_queue", "probe_queue_stats",
		"kretprobe_queue_config", "kprobe_unprep_clone", "kprobe_freeze_queue",
		"kretprobe_stats_config", "kretprobe_stats_config", "kretprobe_stats_config",
		"kretprobe_stats_config",
	}
	for _, definition := range ioLatencyTracepoints {
		programs = append(programs, definition.program)
	}
	object := &fakeIOLatencyAttacher{}
	if err := attachIOLatencyHooks(object, true); err != nil {
		t.Fatal(err)
	}
	if len(object.attached) != 1 || !slices.Equal(object.attached[0], programs) {
		t.Fatalf("attached programs = %v, want %v", object.attached, programs)
	}
	freezeQueueSym := "blk_mq_freeze_queue"
	if !bpf.HasKprobeFunction(freezeQueueSym) {
		freezeQueueSym = "blk_mq_freeze_queue_nomemsave"
	}
	if !slices.ContainsFunc(object.options, func(option bpf.AttachOption) bool {
		return option.ProgramName == "kprobe_freeze_queue" && option.Symbol == freezeQueueSym
	}) {
		t.Fatalf("freeze hook did not select the available symbol %s", freezeQueueSym)
	}
	for _, available := range []bool{false, true} {
		object := &fakeIOLatencyAttacher{}
		if err := attachIOLatencyHooks(object, available); err != nil {
			t.Fatal(err)
		}
		attached := slices.ContainsFunc(object.options, func(option bpf.AttachOption) bool {
			return option.Symbol == "blk_stat_disable_accounting"
		})
		if attached != available {
			t.Fatalf("accounting disable available=%t attached=%t", available, attached)
		}
	}
	for _, program := range programs {
		t.Run(program, func(t *testing.T) {
			failure := errors.New("attach failed")
			object := &fakeIOLatencyAttacher{fail: map[string]error{program: failure}}
			if err := attachIOLatencyHooks(object, true); !errors.Is(err, failure) {
				t.Fatalf("attach error = %v, want %v", err, failure)
			}
		})
	}
}

func assertIOLatencyConstant(
	t *testing.T,
	constants map[string]any,
	name string,
	want uint32,
) {
	t.Helper()
	if name == "" {
		return
	}
	got, ok := constants[name].(uint32)
	if !ok || got != want {
		t.Fatalf("constant %s = %#v, want %d", name, constants[name], want)
	}
}

// Lifecycle: disk admission, configuration, and retirement.

// Disk notifications and configuration scans.

// A notification or lost-record indication wakes one scan. If the reader
// stops, the sequence in the normal status dump still detects later changes.
func TestIOLatencyDiskNotifications(t *testing.T) {
	for _, lost := range []uint64{0, 3} {
		object := &fakeIOLatencyMapBPF{maps: map[string][]bpf.MapItem{}}
		session := &ioLatencySession{object: object, diskEvents: make(chan struct{}, 1)}
		reader := &fakeIOLatencyDiskReader{lost: lost}
		session.readDiskEvents(t.Context(), reader)
		if reader.reads != 3 {
			t.Fatalf("lost=%d: disk reader stopped before the next notification, reads=%d", lost, reader.reads)
		}
		if len(session.diskEvents) != 1 {
			t.Fatalf("lost=%d: notifications did not coalesce to one scan", lost)
		}
		<-session.diskEvents
		object.maps[ioLatencyStatusMap] = []bpf.MapItem{
			ioLatencyMapItem(uint32(1), uint64(7)),
			ioLatencyMapItem(uint32(0), uint64(0)),
		}
		if err := session.checkHealth(); err != nil || !session.disksNeedScan || len(session.diskEvents) != 1 {
			t.Fatalf("change after reader failure: scan=%t, pending=%d, error=%v",
				session.disksNeedScan, len(session.diskEvents), err)
		}
	}
}

// A successful scan acknowledges only changes observed before it began.
// Failed scans and changes during a scan remain eligible for a later refresh.
func TestIOLatencyDiskRefreshSequence(t *testing.T) {
	for _, scenario := range []string{"unchanged", "changed during scan", "failed scan"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			originalPrefix := filepath.Dir(procfs.DefaultPath())
			procfs.RootPrefix(root)
			t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
			path := filepath.Join(root, "sys", "block", "sda", "queue")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "iostats"), []byte("1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			object := &fakeIOLatencyConfigBPF{
				statsEnabled: true,
				fakeIOLatencyDiskBPF: fakeIOLatencyDiskBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
					maps: map[string][]bpf.MapItem{ioLatencyStatusMap: {
						ioLatencyMapItem(uint32(0), uint64(0)), ioLatencyMapItem(uint32(1), uint64(7)),
					}},
				}},
			}
			if scenario == "changed during scan" {
				object.beforeRead = func() {
					object.maps[ioLatencyStatusMap][1] = ioLatencyMapItem(uint32(1), uint64(8))
				}
			} else if scenario == "failed scan" {
				object.readErr = unix.EIO
			}
			session := &ioLatencySession{object: object, diskEvents: make(chan struct{}, 1)}
			err := session.refreshDisks(false)
			if scenario == "failed scan" {
				if !errors.Is(err, unix.EIO) {
					t.Fatalf("scan error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if err := session.checkHealth(); err != nil {
				t.Fatal(err)
			}
			if scenario == "unchanged" {
				if len(session.diskEvents) != 0 {
					t.Fatal("successful scan requested another scan for the same change")
				}
				return
			}
			if len(session.diskEvents) != 1 {
				t.Fatal("unprocessed change did not request a refresh")
			}
			<-session.diskEvents
			object.beforeRead, object.readErr = nil, nil
			if err := session.refreshDisks(false); err != nil {
				t.Fatal(err)
			}
			if err := session.checkHealth(); err != nil || len(session.diskEvents) != 0 {
				t.Fatalf("completed retry left another refresh: %v", err)
			}
		})
	}
}

type fakeIOLatencyDiskReader struct {
	bpf.PerfEventReader
	reads int
	lost  uint64
}

func (r *fakeIOLatencyDiskReader) ReadInto(destination any) error {
	r.reads++
	if r.reads <= 2 {
		if r.reads == 1 && r.lost != 0 {
			return &bpf.PerfEventSamplesLostError{Count: r.lost}
		}
		*destination.(*uint32) = 1
		return nil
	}
	return unix.EIO
}

// Each timestamp can be disabled after enrollment. Reset only that disk's
// IO counters on recovery; freeze counts continue across both transitions.
func TestIOLatencyDiskConfigurationChanges(t *testing.T) {
	for _, timestamp := range []string{"G", "D"} {
		t.Run(timestamp, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "block", "sda", "queue")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			iostats := "1\n"
			if timestamp == "G" {
				iostats = "0\n"
			}
			if err := os.WriteFile(filepath.Join(path, "iostats"), []byte(iostats), 0o600); err != nil {
				t.Fatal(err)
			}
			active := ioLatencyDiskCounters{Major: 259, Minor: 7}
			active.Counters[0].QueuedSize[0] = 17
			freezeRows := []BlkDiskEntry{
				{Disk: 100, Major: 8, FreezeNr: 11},
				{Disk: 200, Major: 259, Minor: 7, FreezeNr: 19},
			}
			object := &fakeIOLatencyConfigBPF{
				statsEnabled: timestamp != "D",
				fakeIOLatencyDiskBPF: fakeIOLatencyDiskBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
					maps: map[string][]bpf.MapItem{
						blkDiskBucketMap: {
							ioLatencyMapItem(uint64(100), ioLatencyDiskCounters{Major: 8}),
							ioLatencyMapItem(uint64(200), active),
						},
						blkDiskLatencyMap: {
							ioLatencyMapItem(uint64(100), freezeRows[0]),
							ioLatencyMapItem(uint64(200), freezeRows[1]),
						},
					},
				}},
			}
			session := &ioLatencySession{object: object, diskEvents: make(chan struct{}, 1), previous: &ioLatencySnapshot{
				host: map[ioLatencyHostKey]*ioLatencyHostSample{{Major: 8}: {}, {Major: 259, Minor: 7}: {}},
			}}
			if disks, err := session.discoverDisks(root, false); err != nil || len(disks) != 0 || len(session.retiredDisks) != 1 {
				t.Fatalf("disabled disk: disks=%v, retired=%v, error=%v", disks, session.retiredDisks, err)
			}
			current, freeze, err := session.captureSnapshot()
			if err != nil || current.host[ioLatencyHostKey{Major: 8}] != nil || len(current.host) != 2 {
				t.Fatalf("paused disk interval: %v, %v", current, err)
			}
			if len(session.previous.host) != 1 || len(session.retiredDisks) != 0 || len(session.diskEvents) != 1 {
				t.Fatal("cleanup did not discard the affected baseline and request re-enrollment")
			}
			if got := current.host[ioLatencyHostKey{Major: 259, Minor: 7}].counters.QueuedSize[0]; got != 17 {
				t.Fatalf("unrelated disk counter = %d", got)
			}
			if !slices.Equal(freeze, freezeRows) {
				t.Fatalf("pause changed freeze counters: %v, want %v", freeze, freezeRows)
			}
			if disks, err := session.discoverDisks(root, false); err != nil || len(disks) != 0 || len(session.retiredDisks) != 0 {
				t.Fatalf("disabled, unenrolled disk requested repeated cleanup: %v, %v", disks, err)
			}
			if err := os.WriteFile(filepath.Join(path, "iostats"), []byte("1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			object.statsEnabled = true
			if disks, err := session.discoverDisks(root, false); err != nil || len(disks) != 1 {
				t.Fatalf("re-enabled disk: %v, %v", disks, err)
			}
			current, freeze, err = session.captureSnapshot()
			if err != nil || current.host[ioLatencyHostKey{Major: 8}].counters != (ioLatencyCounters{}) {
				t.Fatalf("re-enabled disk did not start empty: %v, %v", current, err)
			}
			if !slices.Equal(freeze, freezeRows) {
				t.Fatalf("recovery changed freeze counters: %v, want %v", freeze, freezeRows)
			}
		})
	}
}

// Collection uses the configuration confirmed on entry. Changes observed by
// either health read take effect after the session handles the queued refresh.
func TestIOLatencyDiskChangeAppliesNextCollection(t *testing.T) {
	for _, boundary := range []string{"before snapshot", "after snapshot"} {
		for _, readFailure := range []bool{false, true} {
			name := boundary
			if readFailure {
				name += "/retry probe read"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				originalPrefix := filepath.Dir(procfs.DefaultPath())
				procfs.RootPrefix(root)
				t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
				path := filepath.Join(root, "sys", "block", "sda", "queue")
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "iostats"), []byte("1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				row := ioLatencyDiskCounters{Major: 8}
				row.Counters[0].Buckets[ioLatencyStageQ2D][0] = 15
				other := row
				other.Major, other.Minor = 259, 7
				freeze := []BlkDiskEntry{{Disk: 100, Major: 8, FreezeNr: 11}}
				object := &fakeIOLatencyConfigBPF{
					statsEnabled: true,
					fakeIOLatencyDiskBPF: fakeIOLatencyDiskBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
						maps: map[string][]bpf.MapItem{
							blkDiskBucketMap:  {ioLatencyMapItem(uint64(100), row), ioLatencyMapItem(uint64(200), other)},
							blkDiskLatencyMap: {ioLatencyMapItem(uint64(100), freeze[0])},
						},
					}},
				}
				previous := &ioLatencySnapshot{
					capturedAt: time.Unix(100, 0),
					host: map[ioLatencyHostKey]*ioLatencyHostSample{
						{Major: 8}: {device: "8:0"}, {Major: 259, Minor: 7}: {device: "259:7"},
					},
					containers: map[ioLatencyContainerKey]*ioLatencyContainerSample{
						{Major: 8}: {}, {Major: 259, Minor: 7}: {},
					},
				}
				previous.host[ioLatencyHostKey{Major: 259, Minor: 7}].counters.Buckets[ioLatencyStageQ2D][0] = 10
				session := &ioLatencySession{object: object, previous: previous, diskEvents: make(chan struct{}, 1)}
				collector := &iolatencyTracing{session: session}
				reads := 0
				object.statusHook = func() {
					reads++
					if (boundary == "before snapshot" && reads == 1) || (boundary == "after snapshot" && reads == 2) {
						object.statsEnabled = false
						object.maps[ioLatencyStatusMap] = []bpf.MapItem{
							ioLatencyMapItem(uint32(0), uint64(0)), ioLatencyMapItem(uint32(1), uint64(1)),
						}
						if readFailure {
							object.readErr = unix.EIO
						}
					}
				}
				metrics, err := collector.Update()
				if err != nil {
					t.Fatal(err)
				}
				seriesSize := ioLatencyStageCount*ioLatencyBucketCount + 2*ioSizeBucketCount
				if len(metrics) != 4*seriesSize+3 || len(session.previous.host) != 4 || len(previous.host) != 2 ||
					len(previous.containers) != 2 || len(session.retiredDisks) != 0 || len(session.diskEvents) != 1 {
					t.Fatalf("collection changed its disk configuration: metrics=%d, current=%v, retired=%v, pending=%d",
						len(metrics), session.previous, session.retiredDisks, len(session.diskEvents))
				}
				// Consume the requested refresh as the session loop does between
				// collections. A failed probe keeps the confirmed configuration.
				<-session.diskEvents
				if readFailure {
					if err := session.refreshDisks(false); !errors.Is(err, unix.EIO) {
						t.Fatalf("background refresh error = %v", err)
					}
					if metrics, err = collector.Update(); err != nil || len(metrics) != 4*seriesSize+3 || len(session.diskEvents) != 1 {
						t.Fatalf("failed refresh interrupted collection: metrics=%d, error=%v", len(metrics), err)
					}
					<-session.diskEvents
					object.readErr = nil
				}
				if err := session.refreshDisks(false); err != nil {
					t.Fatal(err)
				}
				beforeCleanup := session.previous
				metrics, err = collector.Update()
				if err != nil {
					t.Fatal(err)
				}
				if len(session.previous.host) != 2 || session.previous.host[ioLatencyHostKey{Major: 8}] != nil ||
					len(beforeCleanup.host) != 2 || beforeCleanup.host[ioLatencyHostKey{Major: 8}] != nil {
					t.Fatalf("changed disk survived after refresh: current=%v, previous=%v", session.previous, beforeCleanup)
				}
				want := metric.NewGaugeData(ioLatencyStageMetrics[0].name, 0, ioLatencyStageMetrics[0].help, map[string]string{
					"device": "259:7", "operation": "read", "le": ioLatencyBucketLabels[0],
				})
				for _, expected := range append(ioLatencyFreezeMetrics(freeze), want) {
					if !slices.ContainsFunc(metrics, func(data *metric.Data) bool { return reflect.DeepEqual(data, expected) }) {
						t.Fatalf("unrelated disk or freeze metric changed: missing %v", expected)
					}
				}
				if len(metrics) != 2*seriesSize+3 {
					t.Fatalf("published %d metrics, want only the other disk, freeze and interval timestamps", len(metrics))
				}
			})
		}
	}
}

// The map-read fake reflects the probe's registered/disabled result and its
// publication into Host counters, while sysfs and cleanup use real code.
type fakeIOLatencyConfigBPF struct {
	fakeIOLatencyDiskBPF
	command      ioLatencyQueueProbe
	statsEnabled bool
	readErr      error
	beforeRead   func()
}

func (b *fakeIOLatencyConfigBPF) MapIDByName(name string) uint32 {
	if name == ioLatencyQueueProbeMap {
		return 100
	}
	return b.fakeIOLatencyDiskBPF.MapIDByName(name)
}

func (b *fakeIOLatencyConfigBPF) WriteMapItems(_ uint32, items []bpf.MapItem) error {
	return decodeBPFMapData(items[0].Value, &b.command)
}

func (b *fakeIOLatencyConfigBPF) DeleteMapItems(id uint32, keys [][]byte) error {
	if id == 100 {
		return nil
	}
	return b.fakeIOLatencyDiskBPF.DeleteMapItems(id, keys)
}

func (b *fakeIOLatencyConfigBPF) ReadMap(_ uint32, _ []byte) ([]byte, error) {
	if b.beforeRead != nil {
		b.beforeRead()
	}
	if b.readErr != nil {
		return nil, b.readErr
	}
	probe := b.command
	probe.Disk, probe.Initial.Major = 100, 8
	probe.Result = 2
	for _, item := range b.maps[blkDiskBucketMap] {
		if bytes.Equal(item.Key, bytesutil.ToBytes(uint64(100))) {
			probe.Registered, probe.Result = 1, 5
		}
	}
	if !b.statsEnabled {
		probe.Result = 3
	} else if probe.Publish != 0 && probe.Registered == 0 {
		b.maps[blkDiskBucketMap] = append(b.maps[blkDiskBucketMap], ioLatencyMapItem(probe.Disk, probe.Initial))
	}
	return bytesutil.ToBytes(probe), nil
}

// Disk-keyed counters, retirement, and device-name reuse.

func TestIOLatencyDiskCounterSnapshot(t *testing.T) {
	row := ioLatencyDiskCounters{Major: 259, Minor: 7}
	row.Counters[0].Buckets[ioLatencyStageQ2D][0] = 17
	row.Counters[1].IssuedSize[0] = 23
	session := &ioLatencySession{object: &fakeIOLatencyMapBPF{
		maps: map[string][]bpf.MapItem{
			blkDiskBucketMap: {ioLatencyMapItem(uint64(0xffff888012340000), row)},
		},
	}}
	samples, _, err := session.captureHostLatency(map[[2]uint32]string{{259, 7}: "nvme1n1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("got %d series, want read and write", len(samples))
	}
	for operation, name := range []string{"read", "write"} {
		key := ioLatencyHostKey{Major: 259, Minor: 7, Operation: uint32(operation)}
		sample := samples[key]
		if sample == nil || sample.device != "nvme1n1" || sample.operation != name ||
			sample.counters != row.Counters[operation] {
			t.Fatalf("%s sample = %#v", name, sample)
		}
	}
}

type fakeIOLatencyDiskBPF struct {
	fakeIOLatencyMapBPF
	deletions []string
	failMap   string
}

var ioLatencyDiskTestMaps = []string{blkDiskBucketMap, blkContainerBucketMap, blkDiskLatencyMap}

func (b *fakeIOLatencyDiskBPF) MapIDByName(name string) uint32 {
	return uint32(slices.Index(ioLatencyDiskTestMaps, name))
}

func (b *fakeIOLatencyDiskBPF) DeleteMapItems(id uint32, keys [][]byte) error {
	name := ioLatencyDiskTestMaps[id]
	b.deletions = append(b.deletions, name)
	if b.failMap == name {
		return unix.EIO
	}
	b.maps[name] = slices.DeleteFunc(b.maps[name], func(item bpf.MapItem) bool {
		return slices.ContainsFunc(keys, func(key []byte) bool { return bytes.Equal(item.Key, key) })
	})
	return nil
}

// Host admission is removed first. A failed subordinate deletion keeps only
// that device suppressed; its next successful cleanup permits fresh counters.
func TestIOLatencyRetiredDiskSnapshot(t *testing.T) {
	for _, failMap := range []string{"", blkDiskBucketMap, blkContainerBucketMap} {
		t.Run("failure-"+failMap, func(t *testing.T) {
			row := ioLatencyDiskCounters{Major: 8, Retired: 1}
			row.Counters[0].QueuedSize[0] = 5
			active := ioLatencyDiskCounters{Major: 259, Minor: 7}
			active.Counters[0].QueuedSize[0] = 17
			oldKey := ioLatencyContainerKey{Blkcg: 1, Major: 8}
			otherKey := ioLatencyContainerKey{Blkcg: 1, Major: 259, Minor: 7}
			object := &fakeIOLatencyDiskBPF{
				failMap: failMap,
				fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{dumpCalls: make(map[string]int), maps: map[string][]bpf.MapItem{
					blkDiskBucketMap: {
						ioLatencyMapItem(uint64(100), row),
						ioLatencyMapItem(uint64(200), active),
					},
					blkContainerBucketMap: {
						ioLatencyMapItem(oldKey, row.Counters[0]),
						ioLatencyMapItem(otherKey, active.Counters[0]),
					},
					blkDiskLatencyMap: {
						// disk_release has already removed the retired disk's freeze row.
						ioLatencyMapItem(uint64(200), BlkDiskEntry{Disk: 200, Major: 259, Minor: 7}),
					},
				}},
			}
			session := &ioLatencySession{object: object, previous: &ioLatencySnapshot{
				host: map[ioLatencyHostKey]*ioLatencyHostSample{
					{Major: 8}: {}, {Major: 259, Minor: 7}: {},
				},
				containers: map[ioLatencyContainerKey]*ioLatencyContainerSample{
					oldKey: {}, otherKey: {},
				},
			}}
			for attempt := 0; attempt < 2; attempt++ {
				before := object.dumpCalls[blkContainerBucketMap]
				current, freeze, err := session.captureSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				if len(current.host) != 2 || current.host[ioLatencyHostKey{Major: 8}] != nil {
					t.Fatalf("retired Host counters exported: %v", current.host)
				}
				if failMap == "" && object.dumpCalls[blkContainerBucketMap]-before != 1 {
					t.Fatal("retirement and collection did not share the container snapshot")
				}
				if len(freeze) != 1 || freeze[0].Major != 259 {
					t.Fatalf("freeze counters = %v", freeze)
				}
				if got := current.host[ioLatencyHostKey{Major: 259, Minor: 7}].counters.QueuedSize[0]; got != 17 {
					t.Fatalf("unrelated counter = %d", got)
				}
				if len(session.previous.host) != 1 || len(session.previous.containers) != 1 {
					t.Fatal("retirement did not isolate affected baselines")
				}
				if attempt == 0 && failMap != "" && len(session.retiredDisks) == 0 {
					t.Fatal("failed cleanup lost its retry identity")
				}
				object.failMap = ""
			}
			if len(session.retiredDisks) != 0 || !session.disksNeedScan {
				t.Fatal("completed cleanup did not permit rediscovery")
			}
			if object.deletions[0] != blkDiskBucketMap {
				t.Fatalf("first deletion = %s", object.deletions[0])
			}
			for _, name := range ioLatencyDiskTestMaps {
				if len(object.maps[name]) != 1 {
					t.Fatalf("remaining %s rows = %d", name, len(object.maps[name]))
				}
			}
			// Re-enrollment may reuse both the pointer and public identity.
			// Its first counter starts at zero, not the retired baseline.
			row.Retired, row.Counters[0].QueuedSize[0] = 0, 2
			object.maps[blkDiskBucketMap] = append(object.maps[blkDiskBucketMap], ioLatencyMapItem(uint64(100), row))
			current, _, err := session.captureSnapshot()
			if err != nil || current.host[ioLatencyHostKey{Major: 8}].counters.QueuedSize[0] != 2 {
				t.Fatalf("re-enrolled disk = %v, %v", current, err)
			}
		})
	}
}

// Both raw pointers of a reused public device are reset, including a new
// pointer enrolled before the old disk's last open reference was released.
func TestIOLatencyReusedDeviceCleanup(t *testing.T) {
	object := &fakeIOLatencyDiskBPF{fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{
		maps: map[string][]bpf.MapItem{blkDiskBucketMap: {
			ioLatencyMapItem(uint64(100), ioLatencyDiskCounters{Major: 8, Retired: 1}),
			ioLatencyMapItem(uint64(200), ioLatencyDiskCounters{Major: 8}),
		}},
	}}
	session := &ioLatencySession{object: object}
	current, _, err := session.captureSnapshot()
	if err != nil || len(current.host) != 0 || len(object.maps[blkDiskBucketMap]) != 0 {
		t.Fatalf("reused device cleanup = %v, %v; remaining %v", current, err, object.maps)
	}
	// Reuse the normal dump to reclaim a late container row whose producer
	// could not complete rollback after Host admission was removed.
	object.maps[blkContainerBucketMap] = []bpf.MapItem{
		ioLatencyMapItem(ioLatencyContainerKey{Blkcg: 1, Major: 8}, ioLatencyCounters{}),
	}
	samples, err := session.captureContainerLatency(map[uint64]*pod.Container{1: {ID: "container"}}, map[[2]uint32]string{})
	if err != nil || len(samples) != 0 || len(object.maps[blkContainerBucketMap]) != 0 {
		t.Fatalf("late container row = %v, %v", samples, err)
	}
}

// Cleanup retries and failed admission preserve unrelated disks.

func TestIOLatencyContainerSnapshotCleanupFailure(t *testing.T) {
	retired := ioLatencyMapItem(ioLatencyContainerKey{Blkcg: 1, Major: 8}, ioLatencyCounters{})
	key := ioLatencyContainerKey{Blkcg: 1, Major: 259, Minor: 7}
	counters := ioLatencyCounters{}
	counters.QueuedSize[0] = 17
	object := &fakeIOLatencyDiskBPF{
		failMap: blkContainerBucketMap,
		fakeIOLatencyMapBPF: fakeIOLatencyMapBPF{maps: map[string][]bpf.MapItem{
			blkContainerBucketMap: {retired, ioLatencyMapItem(key, counters)},
		}},
	}
	session := &ioLatencySession{object: object}
	containers := map[uint64]*pod.Container{1: {
		ID: "container", Labels: map[string]any{ioControlHostNamespaceKey: "host"},
	}}
	devices := map[[2]uint32]string{{259, 7}: "nvme1n1"}
	for _, fail := range []bool{true, false} {
		if !fail {
			object.failMap = ""
		}
		samples, err := session.captureContainerLatency(containers, devices)
		if fail != errors.Is(err, unix.EIO) {
			t.Fatalf("cleanup failure=%t: error=%v", fail, err)
		}
		if len(samples) != 1 || samples[key] == nil || samples[key].counters != counters {
			t.Fatalf("cleanup changed another disk's sample: %v", samples)
		}
	}
}

func TestIOLatencyDiskEnrollmentFailure(t *testing.T) {
	// A failed cleanup for sda must not prevent admitting another device.
	t.Run("unrelated cleanup", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "block", "nvme0c1n1", "queue")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "iostats"), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		session := &ioLatencySession{
			object:       &fakeIOLatencyQueueBPF{result: 2},
			retiredDisks: map[uint64]BlkDiskEntry{100: {Disk: 100, Major: 8}},
		}
		disks, err := session.discoverDisks(root, false)
		if err != nil || len(disks) != 1 || len(session.retiredDisks) != 1 {
			t.Fatalf("unrelated enrollment = %v, %v; pending %v", disks, err, session.retiredDisks)
		}
	})
	for _, failure := range []string{"sysfs", "publish", "disarm"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "block", "sda", "queue")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			object := &fakeIOLatencyQueueBPF{result: 2}
			if failure != "sysfs" {
				if err := os.WriteFile(filepath.Join(path, "iostats"), []byte("1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "publish" {
				object.publishErr = unix.EIO
			} else if failure == "disarm" {
				object.deleteErr = unix.EIO
			}
			session := &ioLatencySession{object: object}
			if _, err := session.discoverDisks(root, false); err == nil {
				t.Fatal("failed discovery reported success")
			}
			if failure == "publish" && len(session.retiredDisks) != 1 {
				t.Fatal("uncertain publication lost its cleanup identity")
			}
			if failure == "disarm" {
				if _, _, err := session.captureSnapshot(); !errors.Is(err, unix.EIO) {
					t.Fatalf("armed publication did not block cleanup: %v", err)
				}
				object.deleteErr = nil
				if err := session.disarmDiskProbe(); err != nil {
					t.Fatal(err)
				}
			}
			if session.diskProbeName != [32]byte{} || object.armed {
				t.Fatal("disk probe remained armed")
			}
		})
	}
}

// Lifecycle: bio/request accounting and clone teardown.

// Request timestamps, remaps, completion ranges, and clone teardown.

func TestIOLatencyRemapAccounting(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "bpf", "iolatency_tracing.c"))
	require.NoError(t, err)
	cache, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "bpf", "include", "bpf_iolatency_cache.h"))
	require.NoError(t, err)
	gc, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "bpf", "iolatency_gc.c"))
	require.NoError(t, err)
	diskHeader, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "bpf", "include", "bpf_iolatency_disk.h"))
	require.NoError(t, err)
	_, diskRelease, found := strings.Cut(string(diskHeader), "SEC(\"kprobe/disk_release\")")
	require.True(t, found)
	diskRelease, _, found = strings.Cut(diskRelease, "\n#endif")
	require.True(t, found)
	section := func(source []byte, start, end string) string {
		_, rest, found := strings.Cut(string(source), start)
		require.True(t, found, "missing production section %s", start)
		body, _, found := strings.Cut(rest, end)
		require.True(t, found, "missing production section end %s", end)
		return start + body
	}
	program := `
#include <assert.h>
#include <errno.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
typedef uint64_t u64;
typedef uint64_t __u64;
typedef uint32_t u32;
typedef uint8_t u8;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
#define __noinline __attribute__((noinline))
#define SEC(name)
#define CORE_READ1(object, field) ({ \
    __auto_type source = (object); \
    source ? (record_kernel_read(&source->field, sizeof(source->field)), source->field) : 0; \
})
#define CORE_READ2(object, first, second) CORE_READ1(CORE_READ1(object, first), second)
#define CORE_SELECT(_1, _2, _3, name, ...) name
#define BPF_CORE_READ(...) CORE_SELECT(__VA_ARGS__, CORE_READ2, CORE_READ1)(__VA_ARGS__)
#define compat_bpf_core_field_offset(field) ((uintptr_t)&(field))
#define bpf_core_field_exists(field) _Generic(&(field), u8 *: bio_has_partno, default: request_has_disk)
#define bio___5_12 bio
#define block_device___5_12 block_device
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
#define COMPAT_BPF_ANY 0
#define COMPAT_BPF_NOEXIST 1
#define REQ_OP_READ 0
#define REQ_OP_WRITE 1
#define REQ_OP_MASK 255
struct hd_struct { struct { u32 devt; } __dev; };
struct disk_part_tbl { struct hd_struct *part[4]; };
struct gendisk {
    u32 major, first_minor;
    struct disk_part_tbl *part_tbl;
};
struct block_device { struct gendisk *bd_disk; u32 bd_dev; };
struct blkcg_gq { void *blkcg; };
struct bio {
    u32 bi_opf;
    struct { u64 bi_size; } bi_iter;
    struct gendisk *bi_disk;
    u8 bi_partno;
    struct block_device *bi_bdev;
    struct blkcg_gq *bi_blkg;
    struct bio *bi_next;
    void *bi_private;
};
struct request_queue { struct gendisk *disk; };
struct request {
    struct bio *bio;
#ifdef IO_LATENCY_TEST_BIO_TAIL_GAP
    u64 tail_padding;
#endif
    struct bio *biotail;
    struct gendisk *rq_disk;
    struct request_queue *q;
    u32 cmd_flags, __data_len;
    u32 stats_sectors;
    u64 start_time_ns, io_start_time_ns;
};
struct pt_regs { void *arg1; };
struct device;
static struct gendisk *io_latency_device_disk(struct device *device)
{ return (struct gendisk *)device; }
struct bpf_raw_tracepoint_args { u64 args[4]; };
struct disk_entry { u64 disk; u32 major, minor; u64 freeze_nr; };
static u32 block_bio_queue_bio_arg, block_bio_remap_bio_arg;
static u32 block_rq_remap_request_arg, block_split_bio_arg;
static u64 io_latency_root_blkcg;
static bool io_latency_containers_enabled = true;
static bool request_has_disk;
static bool bio_has_partno = true;
static u64 clock_ns, status;
static u32 map_calls, clock_reads, host_lookups, status_lookups, freeze_lookups;
static u32 state_lookups, container_admission_lookups;
static long delete_error, update_error;
static bool zero_extend_errno;
static u64 delete_error_key, update_error_key;
static void *update_error_map;
static int bio_latency_map, bio_fallback_map;
static int io_latency_status_map, blkdisk_map;
static int blkdisk_lat_map, blkcg_lat_map, blkcg_map;
static bool track_kernel_reads;
static const void *kernel_reads[256];
static size_t kernel_read_sizes[256];
static u32 kernel_read_count;
static void record_kernel_read(const void *address, size_t size)
{
    if (!track_kernel_reads)
        return;
    assert(kernel_read_count < sizeof(kernel_reads) / sizeof(kernel_reads[0]));
    kernel_reads[kernel_read_count] = address;
    kernel_read_sizes[kernel_read_count++] = size;
}
static u32 kernel_reads_of(const void *address)
{
    u32 count = 0;
    for (u32 i = 0; i < kernel_read_count; i++)
        count += (uintptr_t)address >= (uintptr_t)kernel_reads[i] &&
                 (uintptr_t)address < (uintptr_t)kernel_reads[i] + kernel_read_sizes[i];
    return count;
}
static int bpf_core_read(void *destination, size_t size, const void *source)
{
    record_kernel_read(source, size);
    memcpy(destination, source, size);
    return 0;
}
static u64 bpf_ktime_get_ns(void) { clock_reads++; return clock_ns; }
static struct gendisk *bio_disk(struct bio *bio) { return bio->bi_disk; }
` + section(data, "#define IO_LATENCY_BUCKETS", "#ifndef REQ_OP_MASK") +
		section(cache, "#define IO_LATENCY_BIO_STATES", "struct bio_latency_state {") +
		section(cache, "struct bio_latency_state {", "\n};") + "\n};\n" +
		section(data, "struct latency_counters {", "\nstruct {\n") + `
static struct state_entry {
    u64 key;
    struct bio_latency_state state;
} states[4], fallback_states[4];
static struct host_latency_counters host[256];
static u64 host_keys[256];
static bool host_available[256];
static bool freeze_available[256];
static struct latency_counters container[4][2][256];
static bool container_retirement_test, container_admitted = true;
static bool container_row_present, retire_before_insert, retire_after_insert;
static bool remove_host_on_insert;

static void register_host(struct gendisk *disk)
{
    assert(disk->major < 256);
    host_keys[disk->major] = (u64)disk;
    host[disk->major].major = disk->major;
    host[disk->major].minor = disk->first_minor;
    host_available[disk->major] = true;
    freeze_available[disk->major] = true;
}

static struct state_entry *state_entries(void *map)
{
    assert(map == &bio_latency_map || map == &bio_fallback_map);
    return map == &bio_latency_map ? states : fallback_states;
}

static struct bio_latency_state *map_state_for(void *map, u64 key)
{
    struct state_entry *entries = state_entries(map);
    for (int i = 0; i < 4; i++)
        if (entries[i].key == key)
            return &entries[i].state;
    return NULL;
}

/* Test observations distinguish active IO from retained inactive cache rows. */
static struct bio_latency_state *active_state_for(struct bio *bio)
{
    struct bio_latency_state *state = map_state_for(&bio_latency_map, (u64)bio);
    if (state && state->queue_ns)
        return state;
    state = map_state_for(&bio_fallback_map, (u64)bio);
    return state && state->queue_ns ? state : NULL;
}

static void assert_inactive(struct bio *bio, u64 completed_ns)
{
    struct bio_latency_state *state = map_state_for(&bio_latency_map, (u64)bio);
    assert(state && !state->queue_ns && state->inactive_ns == completed_ns);
    assert(!map_state_for(&bio_fallback_map, (u64)bio));
}

static void *bpf_map_lookup_elem(void *map, const void *key)
{
    map_calls++;
    if (map == &bio_latency_map || map == &bio_fallback_map) {
        state_lookups++;
        return map_state_for(map, *(const u64 *)key);
    }
    if (map == &io_latency_status_map) {
        status_lookups++;
        return &status;
    }
    if (map == &blkdisk_map)
        freeze_lookups++;
    if (map == update_error_map && map != &bio_latency_map)
        return NULL;
    if (map == &blkdisk_lat_map) {
        host_lookups++;
        for (u32 disk = 0; disk < 256; disk++)
            if (host_available[disk] && host_keys[disk] == *(const u64 *)key)
                return &host[disk];
        return NULL;
    }
    if (map == &blkcg_lat_map) {
        const struct container_latency_key *series = key;
        if (container_retirement_test && !container_row_present)
            return NULL;
        return &container[series->blkcg][series->operation][series->major];
    }
    if (map == &blkcg_map) {
        container_admission_lookups++;
        if (!container_admitted || *(const u64 *)key == 3)
            return NULL;
    }
    return &status;
}

/* Direct map calls return int; the 64-bit helper register can expose either
 * zero-extended or sign-extended errno. Exercise both at the helper boundary.
 */
static long map_result(int error)
{
    return zero_extend_errno ? (long)(u32)error : (long)error;
}

static long bpf_map_update_elem(void *map, const void *key,
                                const void *value, u64 flags)
{
    map_calls++;
    if (map == update_error_map &&
        (!update_error_key || *(const u64 *)key == update_error_key))
        return map_result(update_error);
    if (map == &blkdisk_lat_map) {
        for (u32 disk = 0; disk < 256; disk++) {
            if (host_keys[disk] == *(const u64 *)key) {
                host_available[disk] = true;
                host[disk] = *(const struct host_latency_counters *)value;
                return 0;
            }
        }
        return map_result(-ENOENT);
    }
    if (map == &blkcg_lat_map && container_retirement_test) {
        /* Model userspace revoking admission and deleting the row on either
         * side of the actual hash insertion, after BPF's admission lookup.
         */
        if (retire_before_insert) {
            container_admitted = false;
            container_row_present = false;
        }
        container_row_present = true;
        if (remove_host_on_insert)
            host_available[8] = false;
        if (retire_after_insert) {
            container_admitted = false;
            container_row_present = false;
        }
    }
    if (map == &bio_latency_map || map == &bio_fallback_map) {
        struct bio_latency_state *state = map_state_for(map, *(const u64 *)key);
        struct state_entry *entries = state_entries(map);
        if (map == &bio_latency_map) {
            assert(flags == COMPAT_BPF_NOEXIST);
            if (state)
                return map_result(-EEXIST);
        }
        if (!state) {
            for (int i = 0; i < 4; i++) {
                if (!entries[i].key) {
                    entries[i].key = *(const u64 *)key;
                    state = &entries[i].state;
                    break;
                }
            }
        }
        if (!state)
            return map_result(-E2BIG);
        *state = *(const struct bio_latency_state *)value;
    }
    return 0;
}

static long bpf_map_delete_elem(void *map, const void *key)
{
    map_calls++;
    if (map == &blkdisk_map) {
        for (u32 disk = 0; disk < 256; disk++) {
            if (freeze_available[disk] && host_keys[disk] == *(const u64 *)key) {
                freeze_available[disk] = false;
                return 0;
            }
        }
        return map_result(-ENOENT);
    }
    if (map == &blkcg_lat_map) {
        if (delete_error)
            return map_result(delete_error);
        if (!container_row_present)
            return map_result(-ENOENT);
        container_row_present = false;
        return 0;
    }
    struct state_entry *entries = state_entries(map);
    if (delete_error && (!delete_error_key || *(const u64 *)key == delete_error_key))
        return map_result(delete_error);
    for (int i = 0; i < 4; i++)
        if (entries[i].key == *(const u64 *)key) {
            entries[i].key = 0;
            return 0;
        }
    return map_result(-ENOENT);
}
` + section(data, "static __noinline void io_latency_fail", "static __always_inline struct bio_latency_state *io_latency_begin_bio") +
		section(cache, "static __always_inline u32 *io_latency_try_gate", "\n#endif") +
		section(data, "static __always_inline struct bio_latency_state *io_latency_begin_bio", "/* End inactive cache storage. */") +
		section(gc, "static __always_inline int io_latency_reclaim_bio", "/* The session loads") +
		section(data, "static __always_inline u32 io_histogram_above", "SEC(\"kprobe/blk_mq_freeze_queue\")") +
		section(data, "static __noinline u32 queue_bio_latency", "/*\n * Clone teardown") +
		section(data, "SEC(\"kprobe/blk_rq_unprep_clone\")", "\n}\n") + "\n}\n" + diskRelease + `
static void finish_request(struct request *req, u32 bytes)
{
    struct bpf_raw_tracepoint_args event = { .args = { (u64)req, 0, bytes } };
    trace_request_complete(&event);
}

static void test_bucket_boundaries(bool requests)
{
    struct latency_counters host_counters = {}, container_counters = {}, expected = {};
    struct gendisk disk = { .major = 8, .first_minor = 16 };
    struct blkcg_gq group = { .blkcg = (void *)1 };
    struct bio second = { .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group };
    struct bio first = { .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
                         .bi_next = &second };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = { .bio = &first, .biotail = &second, .rq_disk = &disk,
                           .q = (struct request_queue *)&queue, .__data_len = 8192,
                           .io_start_time_ns = 1 };
    struct bpf_raw_tracepoint_args event = {};
    if (requests)
        register_host(&disk);
    const u64 thresholds[] = {
        50000, 100000, 250000, 500000, 1000000, 2000000, 4000000,
        8000000, 16000000, 32000000, 64000000, 128000000, 256000000,
        512000000, 1000000000, 2000000000,
    };
    /* Check both bucket selection and published counters on either side of
     * every threshold, including the fast first bucket and arithmetic path.
     */
    for (int i = 0; i < 16; i++) {
        for (int offset = -1; offset <= 1; offset++) {
            u64 end_ns = 1 + thresholds[i] + offset;
            int bucket = i + (offset > 0);
            assert(io_latency_bucket(1, end_ns) == bucket);
            account_io_latency(&host_counters, &container_counters,
                               IO_LATENCY_STAGE_Q2D, 1, end_ns);
            expected.q2d[bucket]++;
            if (requests) {
                /* The production C entry classifies D2C once for both bios. */
                clock_ns = 2;
                event.args[block_bio_queue_bio_arg] = (u64)&first;
                trace_bio_queue(&event);
                event.args[block_bio_queue_bio_arg] = (u64)&second;
                trace_bio_queue(&event);
                clock_ns = end_ns;
                finish_request(&req, req.__data_len);
                for (int b = 0; b < IO_LATENCY_BUCKETS; b++) {
                    assert(host[8].counters[0].d2c[b] == 2 * expected.q2d[b]);
                    assert(container[1][0][8].d2c[b] == 2 * expected.q2d[b]);
                }
                assert(!status);
            }
        }
    }
    assert(io_latency_bucket(0, 100) == -1);
    assert(io_latency_bucket(100, 100) == -1);
    assert(io_latency_bucket(100, 99) == -1);
    assert(io_latency_bucket(1, UINT64_MAX) == 16);
    account_io_latency(&host_counters, &container_counters, IO_LATENCY_STAGE_Q2D, 0, 100);
    account_io_latency(&host_counters, &container_counters, IO_LATENCY_STAGE_Q2D, 100, 100);
    account_io_latency(&host_counters, &container_counters, IO_LATENCY_STAGE_Q2D, 100, 99);
    account_io_latency(&host_counters, &container_counters, IO_LATENCY_STAGE_Q2D, 1, UINT64_MAX);
    expected.q2d[16]++;
    assert(!memcmp(&host_counters, &expected, sizeof(expected)));
    assert(!memcmp(&container_counters, &expected, sizeof(expected)));
    const u32 sizes[] = { 4096, 16384, 65536, 262144, 1048576 };
    for (int i = 0; i < 5; i++) {
        assert(io_size_bucket(sizes[i]) == i);
        assert(io_size_bucket(sizes[i] + 1) == i + 1);
    }
    assert(io_size_bucket(UINT32_MAX) == 5);
}

static void test_remap(void)
{
    struct gendisk upper = { .major = 9 }, lower = { .major = 8, .first_minor = 16 };
    register_host(&upper);
    register_host(&lower);
    struct blkcg_gq source_group = { .blkcg = (void *)1 };
    struct blkcg_gq target_group = { .blkcg = (void *)2 };
    struct bio io = {
        .bi_opf = REQ_OP_WRITE, .bi_iter.bi_size = 4096,
        .bi_disk = &upper, .bi_blkg = &source_group,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    struct request_queue___iolatency target_queue = { .disk = &lower };
    struct request req = {
        .bio = &io, .rq_disk = &lower,
        .q = (struct request_queue *)&target_queue,
        .cmd_flags = REQ_OP_WRITE, .__data_len = 4096,
    };
    struct bio_latency_state *saved;

    /* A replaces the source device and its queue origin. */
    clock_ns = 1000000000;
    trace_bio_queue(&event);
    io.bi_disk = &lower;
    io.bi_blkg = &target_group;
    clock_ns = 2000000000;
    trace_bio_remap(&event);
    saved = active_state_for(&io);
    assert(saved && saved->major == 8 && saved->minor == 16 && saved->blkcg == 2);
    assert(saved->queue_ns == clock_ns);
    assert(host[9].counters[1].queued_size[0] == 1 && host[8].counters[1].queued_size[0] == 1);

    /* A->A->Q on one whole disk counts once and uses the latest Q/A time. */
    clock_ns = 2020000000;
    trace_bio_remap(&event);
    assert(saved->queue_ns == clock_ns && host[8].counters[1].queued_size[0] == 1);
    clock_ns = 2050000000;
    trace_bio_queue(&event);
    assert(saved->queue_ns == clock_ns && host[8].counters[1].queued_size[0] == 1);
    req.start_time_ns = 2100000000;
    req.io_start_time_ns = 2250000000;
    req.stats_sectors = 8;
    clock_ns = 2400000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&io) && !status);
    assert_inactive(&io, clock_ns);
    assert(host[8].counters[1].q2g[10] == 1);
    assert(host[8].counters[1].q2d[12] == 1);
    assert(host[8].counters[1].d2c[12] == 1 && host[8].counters[1].issued_size[0] == 1);
    assert(!host[9].counters[1].q2d[12] && !host[9].counters[1].d2c[12] && !host[9].counters[1].issued_size[0]);
    assert(memcmp(&host[8].counters[1], &container[2][1][8], sizeof(host[8].counters[1])) == 0);

    /* Completion allows the same bio address to start a fresh Q. */
    clock_ns = 3000000000;
    trace_bio_queue(&event);
    assert(active_state_for(&io)->queue_ns == clock_ns && host[8].counters[1].queued_size[0] == 2);
    /* An invalid request timestamp omits only its own intervals. */
    struct latency_counters before = host[8].counters[1];
    req.start_time_ns = req.io_start_time_ns = 0;
    clock_ns = 3100000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&io) && !memcmp(&before, &host[8].counters[1], sizeof(before)));

    /* A can start tracking a bio that had no observed source Q. */
    clock_ns = 4000000000;
    trace_bio_remap(&event);
    assert(active_state_for(&io) && active_state_for(&io)->queue_ns == clock_ns);
    assert(host[8].counters[1].queued_size[0] == 3);
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&io) && !status);

    /* A flush-sequenced write first completes its data. blk_update_request
     * consumes the bytes but postpones bio_endio until the flush is done.
     */
    clock_ns = 5000000000;
    trace_bio_queue(&event);
    req.start_time_ns = 5010000000;
    req.io_start_time_ns = 5020000000;
    clock_ns = 5100000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&io) && !status);
    struct latency_counters completed_host = host[8].counters[1];
    struct latency_counters completed_container = container[2][1][8];
    io.bi_iter.bi_size = 0;
    req.__data_len = 0;
    req.bio = NULL;

    /* REQ_FSEQ_DONE restores rq->bio from biotail and calls end_request
     * again. The native D/size fields remain set, but this C has no data.
     */
    req.bio = &io;
    clock_ns = 5200000000;
    finish_request(&req, 0);
    assert(!active_state_for(&io) && !status);
    assert(!memcmp(&completed_host, &host[8].counters[1], sizeof(completed_host)));
    assert(!memcmp(&completed_container, &container[2][1][8],
                   sizeof(completed_container)));
}

/* Partition translation does no accounting. A DM/MD remap to the same
 * partition still establishes the lower disk's origin, including devices
 * whose partition dev_t is allocated outside the whole disk's minor range.
 */
static void test_partition_remap(void)
{
    struct hd_struct part = { .__dev.devt = (259U << 20) | 73 };
    struct disk_part_tbl table = { .part = { [3] = &part } };
    struct gendisk disk = {
        .major = 8, .first_minor = 16, .part_tbl = &table,
    };
    register_host(&disk);
    struct block_device bdev = { .bd_disk = &disk, .bd_dev = part.__dev.devt };
    struct bio io = {
        .bi_disk = &disk, .bi_partno = 3, .bi_bdev = &bdev,
        .bi_iter.bi_size = 4096,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_remap_bio_arg] = (u64)&io;
    struct request req = {
        .bio = &io, .rq_disk = &disk, .__data_len = 4096, .stats_sectors = 8,
    };
    struct request_queue___iolatency queue = { .disk = &disk };
    req.q = (struct request_queue *)&queue;

    for (int layout = 0; layout < 2; layout++) {
        bio_has_partno = layout != 0;
        u64 queued = host[8].counters[0].queued_size[0];
        u32 calls = map_calls, clocks = clock_reads;
        event.args[block_bio_remap_bio_arg + 1] = part.__dev.devt;
        clock_ns = 1000000000;
        trace_bio_remap(&event);
        assert(!active_state_for(&io) && host[8].counters[0].queued_size[0] == queued);
        assert(map_calls == calls && clock_reads == clocks);

        /* The ordinary partition IO is recorded at its Q. */
        clock_ns = 2000000000;
        trace_bio_queue(&event);
        assert(active_state_for(&io)->queue_ns == clock_ns);
        assert(host[8].counters[0].queued_size[0] == queued + 1);
        finish_request(&req, 4096);
        assert(!active_state_for(&io));

        event.args[block_bio_remap_bio_arg + 1] = 253U << 20;
        clock_ns = 3000000000;
        trace_bio_remap(&event);
        assert(active_state_for(&io)->queue_ns == clock_ns);
        assert(host[8].counters[0].queued_size[0] == queued + 2);

        /* The following partition translation preserves the DM/MD origin. */
        event.args[block_bio_remap_bio_arg + 1] = part.__dev.devt;
        calls = map_calls;
        clocks = clock_reads;
        clock_ns = 3100000000;
        trace_bio_remap(&event);
        assert(active_state_for(&io)->queue_ns == 3000000000);
        assert(map_calls == calls && clock_reads == clocks);
        finish_request(&req, 4096);
        assert(!active_state_for(&io) && !status);
    }
}

static void test_request_device(void)
{
    struct gendisk upper = { .major = 9 }, lower = { .major = 8, .first_minor = 16 };
    register_host(&upper);
    register_host(&lower);
    struct request_queue___iolatency source_queue = { .disk = &upper };
    struct request_queue___iolatency target_queue = { .disk = &lower };
    struct blkcg_gq first_group = { .blkcg = (void *)1 };
    struct blkcg_gq second_group = { .blkcg = (void *)2 };
    struct bio original = {
        .bi_opf = REQ_OP_WRITE, .bi_iter.bi_size = 4096,
        .bi_disk = &upper, .bi_blkg = &first_group,
    };
    struct bio second = {
        .bi_opf = REQ_OP_READ, .bi_iter.bi_size = 4096,
        .bi_disk = &upper, .bi_blkg = &second_group,
    };
    struct bio first = {
        .bi_opf = REQ_OP_READ, .bi_iter.bi_size = 4096,
        .bi_disk = &upper, .bi_blkg = &first_group, .bi_next = &second,
    };
    struct request req = {
        .bio = &original, .rq_disk = &upper,
        .q = (struct request_queue *)&source_queue,
        .cmd_flags = REQ_OP_WRITE, .__data_len = 4096,
    };
    struct bpf_raw_tracepoint_args queue = {};
    queue.args[block_bio_queue_bio_arg] = (u64)&original;

    /* Upper-layer Q and C do not allocate timing or histogram state. */
    host_available[9] = false;
    clock_ns = 1000000000;
    trace_bio_queue(&queue);
    req.start_time_ns = 1050000000;
    req.io_start_time_ns = 1100000000;
    req.stats_sectors = 8;
    clock_ns = 1200000000;
    finish_request(&req, req.__data_len);
    assert(!host[9].counters[1].issued_size[0] && !active_state_for(&original));

    /* C skips bios without an active Q/A origin, even with valid request
     * timestamps. Request A records the actual dispatch disk and operation.
     */
    req.bio = &first;
    req.rq_disk = &lower;
    req.q = (struct request_queue *)&target_queue;
    req.__data_len = 8192;
    req.stats_sectors = 32;
    req.start_time_ns = 1300000000;
    req.io_start_time_ns = 1400000000;
    clock_ns = 1800000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&first) && !active_state_for(&second));
    assert(!memcmp(&host[8].counters[1], &zero_latency_counters, sizeof(host[8].counters[1])));
    struct bpf_raw_tracepoint_args request_remap = {};
    request_remap.args[block_rq_remap_request_arg] = (u64)&req;
    clock_ns = 2000000000;
    host_available[8] = false;
    trace_request_remap(&request_remap);
    u64 first_key = (u64)&first, second_key = (u64)&second;
    assert(!bpf_map_lookup_elem(&bio_latency_map, &first_key) &&
           !bpf_map_lookup_elem(&bio_latency_map, &second_key) && !status);
    register_host(&lower);
    trace_request_remap(&request_remap);
    req.start_time_ns = 2100000000;
    req.io_start_time_ns = 2200000000;

    /* Partial completion delays issued size until the final C; its first
     * remaining bio supplies container ownership for the original size.
     */
    clock_ns = 2500000000;
    finish_request(&req, 2048);
    assert(active_state_for(&first) && active_state_for(&second));
    assert(!host[8].counters[1].d2c[13] && !host[8].counters[1].issued_size[1]);
    first.bi_iter.bi_size -= 2048;
    req.__data_len -= 2048;
    finish_request(&req, 2048);
    assert(host[8].counters[1].d2c[13] == 1 && !host[8].counters[1].issued_size[1]);
    req.bio = &second;
    req.__data_len -= 2048;
    clock_ns = 2700000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&first) && !active_state_for(&second));
    assert_inactive(&first, 2500000000);
    assert_inactive(&second, clock_ns);
    assert(host[8].counters[1].d2c[13] == 2 && container[1][1][8].d2c[13] == 1 &&
           container[2][1][8].d2c[13] == 1);
    assert(host[8].counters[1].issued_size[1] == 1 && !host[8].counters[0].issued_size[1]);
    assert(!container[1][1][8].issued_size[1] &&
           container[2][1][8].issued_size[1] == 1);
    assert(!host[9].counters[1].issued_size[0] && !host[9].counters[1].issued_size[1]);

    /* C on another disk cannot use queue timestamps from the source disk. */
    host_available[9] = true;
    clock_ns = 3000000000;
    trace_bio_queue(&queue);
    req.bio = &original;
    req.__data_len = 4096;
    req.stats_sectors = 8;
    req.start_time_ns = 3100000000;
    req.io_start_time_ns = 3400000000;
    clock_ns = 3600000000;
    struct latency_counters before = host[8].counters[1];
    finish_request(&req, req.__data_len);
    for (int i = 0; i < IO_LATENCY_BUCKETS; i++)
        assert(host[8].counters[1].q2d[i] == before.q2d[i] &&
               host[8].counters[1].q2g[i] == before.q2g[i]);
    assert(!active_state_for(&original) && !status);

    /* Request A supplies the bottom origin even when the bio still names DM. */
    host_available[9] = false;
    struct bpf_raw_tracepoint_args remap = {};
    remap.args[block_rq_remap_request_arg] = (u64)&req;
    clock_ns = 4000000000;
    trace_request_remap(&remap);
    assert(active_state_for(&original)->major == 8 &&
           active_state_for(&original)->queue_ns == clock_ns);
    clock_ns = 4100000000;
    trace_request_remap(&remap);
    assert(active_state_for(&original)->queue_ns == clock_ns &&
           host[8].counters[1].queued_size[0] == 3);
    req.start_time_ns = 4150000000;
    req.io_start_time_ns = 4160000000;
    clock_ns = 4200000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&original) && host[8].counters[1].q2d[10] == 1 && !status);
}

/* C retains a partially completed bio, then settles each complete bio once.
 * Kernel advancement between events is separate from the traced function.
 */
static void test_partial_completion(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq first_group = { .blkcg = (void *)1 };
    struct blkcg_gq second_group = { .blkcg = (void *)2 };
    struct bio second = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &second_group,
    };
    struct bio first = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &first_group,
        .bi_next = &second,
    };
    struct request req = {
        .bio = &first, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 8192, .stats_sectors = 16,
        .start_time_ns = 1030000000, .io_start_time_ns = 1050000000,
    };
    struct bpf_raw_tracepoint_args remap = {};
    remap.args[block_rq_remap_request_arg] = (u64)&req;
    clock_ns = 1000000000;
    trace_request_remap(&remap);

    clock_ns = 1100000000;
    finish_request(&req, 2048);
    assert(active_state_for(&first) && active_state_for(&second));
    assert(!host[8].counters[0].issued_size[1]);
    for (int i = 0; i < IO_LATENCY_BUCKETS; i++)
        assert(!host[8].counters[0].q2d[i] && !host[8].counters[0].d2c[i]);

    first.bi_iter.bi_size -= 2048;
    req.__data_len -= 2048;
    clock_ns = 1120000000;
    finish_request(&req, 2048);
    assert(!active_state_for(&first) && active_state_for(&second));
    assert_inactive(&first, clock_ns);
    assert(host[8].counters[0].q2d[10] == 1 && !host[8].counters[0].issued_size[1]);

    req.bio = &second;
    req.__data_len -= 2048;
    clock_ns = 1140000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&second) && !status);
    assert_inactive(&second, clock_ns);
    assert(host[8].counters[0].d2c[11] == 2);
    assert(host[8].counters[0].q2g[9] == 2 && host[8].counters[0].q2d[10] == 2);
    assert(host[8].counters[0].issued_size[1] == 1);
    assert(!container[1][0][8].issued_size[1] &&
           container[2][0][8].issued_size[1] == 1);
}

/* Exercise accounting across several completion chunks and the end of a
 * 512-bio walk. Only four bios need start records to observe traversal.
 * A partial C stops at its current bio; final C accounts issued size once.
 */
static void test_completion_chunks(bool partial)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq first_group = { .blkcg = (void *)1 };
    struct blkcg_gq second_group = { .blkcg = (void *)2 };
    struct bio chain[IO_LATENCY_REQUEST_SCAN_BUDGET] = {};
    const u32 count = partial ? 65 : IO_LATENCY_REQUEST_SCAN_BUDGET;
    const u32 tracked[] = { 0, 32, partial ? 33 : 480, count - 1 };
    struct request req = {
        .bio = chain, .biotail = &chain[count - 1], .rq_disk = &disk,
        .q = (struct request_queue *)&queue,
        .__data_len = count * 4096, .stats_sectors = count * 8,
        .start_time_ns = 1030000000, .io_start_time_ns = 1050000000,
    };
    struct bpf_raw_tracepoint_args event = {};
    for (u32 i = 0; i < count; i++) {
        chain[i].bi_iter.bi_size = 4096;
        chain[i].bi_disk = &disk;
        if (i + 1 < count)
            chain[i].bi_next = &chain[i + 1];
    }
    clock_ns = 1000000000;
    for (u32 i = 0; i < 4; i++) {
        chain[tracked[i]].bi_blkg = i < 2 ? &first_group : &second_group;
        event.args[block_bio_queue_bio_arg] = (u64)&chain[tracked[i]];
        trace_bio_queue(&event);
    }
    clock_ns = 1100000000;
    if (partial) {
        const u32 bytes = 33 * 4096 + 2048;
        finish_request(&req, bytes);
        assert(!active_state_for(&chain[0]) && !active_state_for(&chain[32]));
        assert(active_state_for(&chain[33]) && active_state_for(&chain[count - 1]));
        assert(host[8].counters[0].d2c[10] == 2);
        for (u32 bucket = 0; bucket < IO_SIZE_BUCKETS; bucket++)
            assert(!host[8].counters[0].issued_size[bucket]);
        /* Apply the kernel's byte/bio advancement between the two events. */
        req.bio = &chain[33];
        req.__data_len -= bytes;
        chain[33].bi_iter.bi_size -= 2048;
        clock_ns = 1200000000;
    }
    finish_request(&req, req.__data_len);
    assert(!status);
    for (u32 i = 0; i < 4; i++) {
        assert(!active_state_for(&chain[tracked[i]]));
        assert_inactive(&chain[tracked[i]], partial && i < 2 ? 1100000000 : clock_ns);
    }
    assert(host[8].counters[0].q2g[9] == 4 && host[8].counters[0].q2d[10] == 4);
    assert(host[8].counters[0].d2c[10] == (partial ? 2 : 4));
    assert(host[8].counters[0].d2c[12] == (partial ? 2 : 0));
    const u32 issued_bucket = partial ? 4 : 5;
    assert(host[8].counters[0].issued_size[issued_bucket] == 1);
    assert(container[1][0][8].issued_size[issued_bucket] == !partial);
    assert(container[2][0][8].issued_size[issued_bucket] == partial);
    for (u32 group = 1; group <= 2; group++) {
        assert(container[group][0][8].q2g[9] == 2);
        assert(container[group][0][8].q2d[10] == 2);
    }
}

static void test_split(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq group = { .blkcg = (void *)1 };
    struct bio parent = {
        .bi_opf = REQ_OP_WRITE, .bi_iter.bi_size = 4096,
        .bi_disk = &disk, .bi_blkg = &group,
    };
    struct bio child = {
        .bi_opf = REQ_OP_WRITE, .bi_iter.bi_size = 2048,
        .bi_disk = &disk, .bi_blkg = &group, .bi_private = &parent,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&parent;
    clock_ns = 1000000000;
    trace_bio_queue(&event);
    parent.bi_iter.bi_size = 2048;
    event.args[block_split_bio_arg] = (u64)&child;
    clock_ns = 1050000000;

    /* A disk may be paused after Q, while the parent waits to be split. */
    host_available[8] = false;
    trace_bio_split(&event);
    u64 child_key = (u64)&child;
    assert(!bpf_map_lookup_elem(&bio_latency_map, &child_key) && !status);

    register_host(&disk);
    kernel_read_count = 0;
    track_kernel_reads = true;
    trace_bio_split(&event);
    track_kernel_reads = false;
    assert(!kernel_reads_of(&disk.major) && !kernel_reads_of(&disk.first_minor));
    assert(active_state_for(&child)->queue_ns == active_state_for(&parent)->queue_ns);
    assert(active_state_for(&child)->major == 8 && active_state_for(&child)->blkcg == 1);
    assert(host[8].counters[1].queued_size[0] == 1);

    /* Each bottom request completes its own bio, independent of bio_chain. */
    struct request req = {
        .bio = &child, .rq_disk = &disk,
        .q = (struct request_queue *)&queue,
        .cmd_flags = REQ_OP_WRITE, .__data_len = 2048,
        .stats_sectors = 4, .start_time_ns = 1020000000,
        .io_start_time_ns = 1050000000,
    };
    clock_ns = 1100000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&child) && active_state_for(&parent));
    req.bio = &parent;
    clock_ns = 1200000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&parent) && !status);
    assert(host[8].counters[1].q2d[10] == 2);
    assert(host[8].counters[1].d2c[10] == 1 && host[8].counters[1].d2c[12] == 1);
    assert(memcmp(&host[8].counters[1], &container[1][1][8], sizeof(host[8].counters[1])) == 0);
}

static void test_clone_cleanup(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq old_group = { .blkcg = (void *)1 };
    struct blkcg_gq new_group = { .blkcg = (void *)2 };
    struct bio untracked = {};
    struct bio second = {
        .bi_opf = REQ_OP_WRITE, .bi_iter.bi_size = 2048,
        .bi_disk = &disk, .bi_blkg = &old_group,
    };
    struct bio first = {
        .bi_opf = REQ_OP_WRITE, .bi_iter.bi_size = 2048,
        .bi_disk = &disk, .bi_blkg = &old_group, .bi_next = &second,
    };
    struct request req = {
        .bio = &first, .rq_disk = &disk,
        .q = (struct request_queue *)&queue,
        .cmd_flags = REQ_OP_WRITE, .__data_len = 4096,
    };
    struct bpf_raw_tracepoint_args remap = {};
    remap.args[block_rq_remap_request_arg] = (u64)&req;
    struct pt_regs cleanup = { .arg1 = &req };

    /* A clone can be released without C. Reclamation preserves Q/A samples
     * and does not publish latency or issued-size observations.
     */
    clock_ns = 1000000000;
    trace_request_remap(&remap);
    req.start_time_ns = 1050000000;
    req.io_start_time_ns = 1100000000;
    req.stats_sectors = 8;
    assert(active_state_for(&first) && active_state_for(&second));
    struct latency_counters queued = host[8].counters[1];
    second.bi_next = &untracked;
    kprobe_unprep_clone(&cleanup);
    assert(!active_state_for(&first) && !active_state_for(&second) && !status);
    assert_inactive(&first, clock_ns);
    assert_inactive(&second, clock_ns);
    assert(req.bio == &first && first.bi_next == &second &&
           second.bi_next == &untracked);
    assert(memcmp(&queued, &host[8].counters[1], sizeof(queued)) == 0);

    /* Reused clone addresses get a fresh Q and container attribution. */
    first.bi_next = NULL;
    first.bi_blkg = &new_group;
    req.__data_len = 2048;
    req.stats_sectors = 4;
    clock_ns = 2000000000;
    trace_request_remap(&remap);
    assert(active_state_for(&first)->blkcg == 2 &&
           active_state_for(&first)->queue_ns == clock_ns);
    req.start_time_ns = 2050000000;
    req.io_start_time_ns = 2100000000;
    clock_ns = 2400000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&first) && !status);
    assert(host[8].counters[1].issued_size[0] == 1 && host[8].counters[1].d2c[13] == 1);
    assert(!container[1][1][8].issued_size[0] &&
           !container[1][1][8].d2c[13] && container[2][1][8].d2c[13] == 1 &&
           container[2][1][8].issued_size[0] == 1);

    /* A normally completed request has an empty chain: no map or clock work. */
    req.bio = NULL;
    map_calls = clock_reads = 0;
    kprobe_unprep_clone(&cleanup);
    assert(!map_calls && !clock_reads && !status);
}

static void test_clone_cleanup_error(void)
{
    struct bio clone = {};
    struct request req = { .bio = &clone };
    struct pt_regs cleanup = { .arg1 = &req };

    delete_error = -EIO;
    kprobe_unprep_clone(&cleanup);
    assert(status >> 32 == IO_LATENCY_STATE_DELETE_FAILED);
    assert((int32_t)status == -EIO);
}

static void test_busy_state_writes(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq group = { .blkcg = (void *)1 };
    struct bio second = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
    };
    struct bio first = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
        .bi_next = &second,
    };
    struct request req = {
        .bio = &first, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 8192,
    };
    struct bpf_raw_tracepoint_args event = {}, remap = {};
    struct pt_regs cleanup = { .arg1 = &req };
    event.args[block_bio_queue_bio_arg] = (u64)&first;
    remap.args[block_rq_remap_request_arg] = (u64)&req;

    /* A busy cache write uses fallback storage; later bios still progress. */
    clock_ns = 1000000000;
    update_error_map = &bio_latency_map;
    update_error_key = (u64)&first;
    update_error = -EBUSY;
    trace_bio_queue(&event);
    assert(map_state_for(&bio_fallback_map, (u64)&first) && !status);
    trace_request_remap(&remap);
    assert(active_state_for(&first) && active_state_for(&second) && !status);
    req.start_time_ns = 1010000000;
    req.io_start_time_ns = 1020000000;
    req.stats_sectors = 16;
    clock_ns += 100000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&first) && !active_state_for(&second) &&
           host[8].counters[0].d2c[11] == 2 && !status);
    assert_inactive(&second, clock_ns);

    assert(io_latency_reclaim_bio((u64)&second, clock_ns, clock_ns) == 1);
    update_error_map = NULL;
    clock_ns = 2000000000;
    trace_bio_queue(&event);
    second.bi_private = &first;
    event.args[block_split_bio_arg] = (u64)&second;
    update_error_map = &bio_latency_map;
    update_error_key = (u64)&second;
    trace_bio_split(&event);
    assert(map_state_for(&bio_fallback_map, (u64)&second) && !status);
    update_error_map = NULL;
    trace_bio_split(&event);
    assert(active_state_for(&second)->queue_ns == clock_ns);

    /* Contention routes a new cycle to fallback insert/delete storage. */
    kprobe_unprep_clone(&cleanup);
    u32 *held = io_latency_try_gate(map_state_for(&bio_latency_map, (u64)&first));
    assert(held);
    event.args[block_bio_queue_bio_arg] = (u64)&first;
    trace_bio_queue(&event);
    io_latency_release_gate(held);
    event.args[block_bio_queue_bio_arg] = (u64)&second;
    trace_bio_queue(&event);
    assert(map_state_for(&bio_fallback_map, (u64)&first));

    /* Busy fallback deletion retains that record without stopping C. */
    delete_error = -EBUSY;
    delete_error_key = (u64)&first;
    req.start_time_ns = clock_ns + 10000000;
    req.io_start_time_ns = clock_ns + 20000000;
    clock_ns += 100000000;
    finish_request(&req, req.__data_len);
    assert(active_state_for(&first) && !active_state_for(&second) && !status);
    assert(host[8].counters[0].q2d[9] == 4);

    /* Q overwrites the retained time; clone cleanup handles the next bio. */
    clock_ns = 3000000000;
    event.args[block_bio_queue_bio_arg] = (u64)&first;
    trace_bio_queue(&event);
    assert(active_state_for(&first)->queue_ns == clock_ns);
    event.args[block_bio_queue_bio_arg] = (u64)&second;
    trace_bio_queue(&event);
    kprobe_unprep_clone(&cleanup);
    assert(active_state_for(&first) && !active_state_for(&second) && !status);
    delete_error = 0;
    kprobe_unprep_clone(&cleanup);
    assert(!active_state_for(&first) && !status);

    /* A busy fallback drops the write; a full fallback stops collection. */
    update_error_map = &bio_fallback_map;
    update_error_key = (u64)&first;
    update_error = -EBUSY;
    held = io_latency_try_gate(map_state_for(&bio_latency_map, (u64)&first));
    assert(held);
    event.args[block_bio_queue_bio_arg] = (u64)&first;
    trace_bio_queue(&event);
    assert(!active_state_for(&first) && !status);
    update_error = -E2BIG;
    trace_bio_queue(&event);
    io_latency_release_gate(held);
    assert(status >> 32 == IO_LATENCY_STATE_INSERT_FAILED);
    assert((int32_t)status == -E2BIG);

    /* Userspace owns shutdown. Until detach, later IO can still complete
     * and reclaim its state while the recorded error remains available.
     */
    u64 failure = status;
    update_error_map = NULL;
    trace_bio_queue(&event);
    assert(active_state_for(&first) && status == failure);
    req.bio = &first;
    req.__data_len = 4096;
    req.stats_sectors = 8;
    req.start_time_ns = clock_ns + 1;
    req.io_start_time_ns = clock_ns + 2;
    clock_ns += 100000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&first) && status == failure);
    assert_inactive(&first, clock_ns);
}

static void test_busy_series_write(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq group = { .blkcg = (void *)1 };
    struct bio io = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
    };
    struct bpf_raw_tracepoint_args event = {};
    struct request req = {
        .bio = &io, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1010000000, .io_start_time_ns = 1020000000,
    };
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    clock_ns = 1000000000;
    update_error_map = &blkcg_lat_map;
    update_error = -EBUSY;
    trace_bio_queue(&event);
    assert(active_state_for(&io) && !status);
    assert(host[8].counters[0].queued_size[0] == 1);
    assert(!container[1][0][8].queued_size[0]);
    update_error_map = NULL;
    clock_ns += 100000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&io) && !status);
    assert(host[8].counters[0].q2d[9] == 1 && container[1][0][8].q2d[9] == 1);
}

static void test_container_allocation_failure(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq group = { .blkcg = (void *)1 };
    struct bio io = { .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1000000001, .io_start_time_ns = 1000000002,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    update_error_map = &blkcg_lat_map;
    update_error = -ENOMEM;
    clock_ns = 1000000000;
    trace_bio_queue(&event);
    clock_ns += 3;
    finish_request(&req, req.__data_len);
    assert(!status && !active_state_for(&io));
    assert(host[8].counters[0].queued_size[0] == 1 && host[8].counters[0].issued_size[0] == 1);
    assert(host[8].counters[0].q2g[0] == 1 && host[8].counters[0].q2d[0] == 1);
    assert(host[8].counters[0].d2c[0] == 1);
    assert(!memcmp(&container[1][0][8], &zero_latency_counters, sizeof(zero_latency_counters)));
}

static void test_container_retirement(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct container_latency_key key = { .blkcg = 1, .major = 8 };
    container_retirement_test = true;
    for (int before = 0; before < 2; before++) {
        container_admitted = true;
        container_row_present = false;
        retire_before_insert = before;
        retire_after_insert = !before;
        assert(!lookup_container_counters(&key, &disk));
        assert(!container_row_present && !status);
    }
    /* A new admission can create and reuse its row normally. */
    retire_before_insert = retire_after_insert = false;
    container_admitted = true;
    assert(lookup_container_counters(&key, &disk) == &container[1][0][8]);
    assert(container_row_present && !status);
    u32 calls = map_calls;
    assert(lookup_container_counters(&key, &disk) == &container[1][0][8]);
    assert(map_calls - calls == 2);

    /* Disk retirement closes admission before deleting container counters.
     * A creator already past its first Host lookup removes its late row.
     */
    container_row_present = false;
    remove_host_on_insert = true;
    assert(!lookup_container_counters(&key, &disk));
    assert(!container_row_present && !status);
    remove_host_on_insert = false;
    register_host(&disk);

    /* A failed container rollback cannot invalidate independent Host data. */
    container_row_present = false;
    retire_before_insert = true;
    delete_error = -EIO;
    assert(!lookup_container_counters(&key, &disk));
    assert(container_row_present);
    assert(!status);
}

/* Retirement marks the retained row. Q/C have no retirement branch and
 * still account until userspace removes Host admission; another disk runs.
 */
static void test_disk_retirement(void)
{
    struct gendisk disk = { .major = 8 };
    struct gendisk other = { .major = 9 };
    register_host(&disk);
    register_host(&other);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct bio io = { .bi_iter.bi_size = 4096, .bi_disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk,
        .q = (struct request_queue *)&queue, .stats_sectors = 8,
        .__data_len = 4096, .start_time_ns = 110, .io_start_time_ns = 120,
    };
    struct pt_regs release = { .arg1 = &disk };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    kprobe_disk_release(&release);
    assert(host[8].retired == 1);
    assert(!freeze_available[8] && freeze_available[9]);
    clock_ns = 100;
    trace_bio_queue(&event);
    clock_ns = 130;
    finish_request(&req, 4096);
    assert(host[8].counters[0].queued_size[0] == 1);
    assert(host[8].counters[0].d2c[0] == 1 && !status);

    host_available[8] = false;
    struct host_latency_counters before = host[8];
    clock_ns = 200;
    trace_bio_queue(&event);
    finish_request(&req, 4096);
    assert(!memcmp(&before, &host[8], sizeof(before)));
    assert(!active_state_for(&io) && !status);

    /* A paused disk retains freeze even though its Host row is absent.
     * Final release must reclaim it independently of latency admission.
     */
    freeze_available[8] = true;
    kprobe_disk_release(&release);
    assert(!freeze_available[8] && freeze_available[9]);

    disk.major = 9;
    register_host(&disk);
    trace_bio_queue(&event);
    assert(host[9].counters[0].queued_size[0] == 1);
}

/* All three intervals settle at C. Requeue replaces the native D timestamp;
 * unavailable or out-of-order timestamps omit only the invalid interval.
 */
static void test_request_time(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct bio io = { .bi_iter.bi_size = 4096, .bi_disk = &disk };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .rq_disk = &disk, .__data_len = 4096,
        .q = (struct request_queue *)&queue,
        .stats_sectors = 8,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    for (int test = 0; test < 4; test++) {
        clock_ns = 1000000000;
        trace_bio_queue(&event);
        req.start_time_ns = test == 1 ? 0 :
            (test == 2 ? 999000000 : 1010000000);
        req.io_start_time_ns = 1020000000;
        /* The request is issued again without a new Q. Only last D is used. */
        req.io_start_time_ns = test == 3 ? 1050000000 : 1030000000;
        clock_ns = 1040000000;
        finish_request(&req, 4096);
        assert(!active_state_for(&io) && !status);
    }
    assert(host[8].counters[0].q2g[8] == 2);
    assert(host[8].counters[0].q2d[9] == 3 && host[8].counters[0].q2d[10] == 1);
    assert(host[8].counters[0].d2c[8] == 3);
    assert(host[8].counters[0].issued_size[0] == 4);
}

/* Root blkcg has no container row. Knowing its address skips admission
 * lookups while preserving all host samples; an unavailable symbol uses
 * normal admission lookup instead.
 */
static void test_root_blkcg(bool known)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq group = { .blkcg = (void *)3 };
    struct bio io = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
    };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk,
        .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1000000001, .io_start_time_ns = 1000000002,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    io_latency_root_blkcg = known ? 3 : 0;
    clock_ns = 1000000000;
    trace_bio_queue(&event);
    clock_ns = 1000000003;
    finish_request(&req, req.__data_len);
    assert(host[8].counters[0].queued_size[0] == 1 && host[8].counters[0].issued_size[0] == 1);
    assert(host[8].counters[0].q2g[0] == 1 && host[8].counters[0].q2d[0] == 1);
    assert(host[8].counters[0].d2c[0] == 1 && !status);
    assert(!memcmp(&container[3][0][8], &zero_latency_counters,
                   sizeof(container[3][0][8])));
    assert(container_admission_lookups == (known ? 0 : 2));
    assert_inactive(&io, clock_ns);

    /* A known root address must not bypass an ordinary container's row. */
    group.blkcg = (void *)1;
    clock_ns = 2000000000;
    req.start_time_ns = clock_ns + 1;
    req.io_start_time_ns = clock_ns + 2;
    trace_bio_queue(&event);
    clock_ns += 3;
    finish_request(&req, req.__data_len);
    assert(container_admission_lookups == (known ? 2 : 4));
    assert(container[1][0][8].queued_size[0] == 1);
    assert(container[1][0][8].issued_size[0] == 1);
    assert(container[1][0][8].q2g[0] == 1);
    assert(container[1][0][8].q2d[0] == 1 && container[1][0][8].d2c[0] == 1);
    assert(host[8].counters[0].d2c[0] == 2 && !status);
}

static void test_host_only(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq group = { .blkcg = (void *)1 };
    struct bio io = {
        .bi_opf = REQ_OP_READ, .bi_iter.bi_size = 4096,
        .bi_disk = &disk, .bi_blkg = &group,
    };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1000000001, .io_start_time_ns = 1000000002,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    io_latency_containers_enabled = false;
    status_lookups = freeze_lookups = 0;
    clock_ns = 1000000000;
    trace_bio_queue(&event);
    assert(active_state_for(&io)->blkcg == 0);
    assert(host[8].counters[0].queued_size[0] == 1);
    assert(container[1][0][8].queued_size[0] == 0);
    trace_bio_remap(&event);
    assert(host[8].counters[0].queued_size[0] == 1);
    clock_ns = 1000000003;
    finish_request(&req, req.__data_len);
    assert(host[8].counters[0].q2d[0] == 1);
    assert(container[1][0][8].q2d[0] == 0);
    assert(!active_state_for(&io) && !status);
    assert(status_lookups == 0 && freeze_lookups == 0);
}

/* Only startup-qualified disks have histogram rows. Q/A and C must leave a
 * newly observed disk alone until it can be checked by another session.
 */
static void test_unselected_disk(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq group = { .blkcg = (void *)1 };
    struct bio io = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
    };
    struct request req = {
        .bio = &io, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1010000000, .io_start_time_ns = 1020000000,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    host_available[8] = false;
    clock_ns = 1000000000;
    trace_bio_queue(&event);
    trace_bio_remap(&event);
    /* An unadmitted disk does not enter the bounded request walk. */
    struct bio chain[IO_LATENCY_REQUEST_SCAN_BUDGET + 1] = {};
    for (int i = 0; i < IO_LATENCY_REQUEST_SCAN_BUDGET; i++)
        chain[i].bi_next = &chain[i + 1];
    req.bio = chain;
    struct bpf_raw_tracepoint_args remap = {};
    remap.args[block_rq_remap_request_arg] = (u64)&req;
    trace_request_remap(&remap);
    req.bio = &io;
    clock_ns = 1100000000;
    finish_request(&req, req.__data_len);
    assert(!active_state_for(&io) && !host_available[8] && !status);
    assert(!memcmp(&host[8].counters[0], &zero_latency_counters, sizeof(host[8].counters[0])));
    assert(!memcmp(&container[1][0][8], &zero_latency_counters,
                   sizeof(container[1][0][8])));
}

/* One C shares request timestamps, device identity, and the host series
 * across its bios. Field-address accounting catches repeated CO-RE reads.
 */
static void test_completion_reads_once(void)
{
    struct gendisk disk = { .major = 8, .first_minor = 16 };
    register_host(&disk);
    struct request_queue___iolatency queue = { .disk = &disk };
    struct blkcg_gq first_group = { .blkcg = (void *)1 };
    struct blkcg_gq second_group = { .blkcg = (void *)2 };
    struct bio second = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &second_group,
    };
    struct bio first = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &first_group,
        .bi_next = &second,
    };
    struct request req = {
        .bio = &first, .rq_disk = &disk, .q = (struct request_queue *)&queue,
        .__data_len = 8192, .stats_sectors = 16,
        .start_time_ns = 1030000000, .io_start_time_ns = 1050000000,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&first;
    clock_ns = 1000000000;
    trace_bio_queue(&event);
    event.args[block_bio_queue_bio_arg] = (u64)&second;
    clock_ns = 1010000000;
    trace_bio_queue(&event);
    clock_ns = 1080000000;
    clock_reads = host_lookups = state_lookups = 0;
    track_kernel_reads = true;
    finish_request(&req, req.__data_len);
    track_kernel_reads = false;

    assert(!active_state_for(&first) && !active_state_for(&second) && !status);
    assert(host[8].counters[0].q2g[9] == 2 && host[8].counters[0].q2d[10] == 2);
    assert(host[8].counters[0].d2c[9] == 2);
    assert(host[8].counters[0].issued_size[1] == 1);
    assert(clock_reads == 1 && host_lookups == 1);
    assert(state_lookups == 2);
    assert(kernel_read_count == (request_has_disk ? 9 : 10));
    assert(kernel_reads_of(&req.bio) == 1);
    assert(kernel_reads_of(&req.cmd_flags) == 1);
    assert(kernel_reads_of(&req.__data_len) == 1);
    assert(kernel_reads_of(&req.stats_sectors) == 1);
    assert(kernel_reads_of(&req.start_time_ns) == 1);
    assert(kernel_reads_of(&req.io_start_time_ns) == 1);
    assert(kernel_reads_of(&disk.major) == 0);
    assert(kernel_reads_of(&disk.first_minor) == 0);
    assert(kernel_reads_of(&first.bi_iter.bi_size) == 1);
    assert(kernel_reads_of(&second.bi_iter.bi_size) == 1);
    if (request_has_disk)
        assert(kernel_reads_of(&req.rq_disk) == 1 && !kernel_reads_of(&req.q));
    else
        assert(kernel_reads_of(&req.q) == 1 && kernel_reads_of(&queue.disk) == 1);
    for (u32 i = 0; i < kernel_read_count; i++)
        assert(kernel_reads_of(kernel_reads[i]) == 1);
}

/* Partial C keeps a single bio active. Its final C can use the request's
 * byte count and adjacent head/tail pointers without rereading bio size.
 */
static void test_single_bio_completion(void)
{
    struct gendisk disk = { .major = 8 };
    register_host(&disk);
    struct blkcg_gq group = { .blkcg = (void *)1 };
    struct bio io = {
        .bi_iter.bi_size = 4096, .bi_disk = &disk, .bi_blkg = &group,
    };
    struct request_queue___iolatency queue = { .disk = &disk };
    struct request req = {
        .bio = &io, .biotail = &io, .rq_disk = &disk,
        .q = (struct request_queue *)&queue,
        .__data_len = 4096, .stats_sectors = 8,
        .start_time_ns = 1000000001, .io_start_time_ns = 1000000002,
    };
    struct bpf_raw_tracepoint_args event = {};
    event.args[block_bio_queue_bio_arg] = (u64)&io;
    clock_ns = 1000000000;
    trace_bio_queue(&event);
    track_kernel_reads = true;
    clock_ns = 1000000003;
    finish_request(&req, 1024);
    assert(active_state_for(&io) && !host[8].counters[0].d2c[0]);
    assert(kernel_reads_of(&io.bi_iter.bi_size) == 1);

    /* Model the kernel's advancement after that partial completion. */
    io.bi_iter.bi_size -= 1024;
    req.__data_len -= 1024;
    kernel_read_count = 0;
    clock_ns++;
    finish_request(&req, req.__data_len);
    assert_inactive(&io, clock_ns);
    assert(!status && host[8].counters[0].q2g[0] == 1);
    assert(host[8].counters[0].q2d[0] == 1 && host[8].counters[0].d2c[0] == 1);
    assert(host[8].counters[0].issued_size[0] == 1);
    assert(!memcmp(&host[8].counters[0], &container[1][0][8], sizeof(host[8].counters[0])));
    assert(kernel_reads_of(&req.bio) == 1);
#ifdef IO_LATENCY_TEST_BIO_TAIL_GAP
    assert(kernel_reads_of(&req.biotail) == 0);
    assert(kernel_reads_of(&io.bi_iter.bi_size) == 1);
    assert(kernel_read_count == (request_has_disk ? 7 : 8));
#else
    assert(kernel_reads_of(&req.biotail) == 1);
    assert(kernel_reads_of(&io.bi_iter.bi_size) == 0);
    assert(kernel_read_count == (request_has_disk ? 6 : 7));
#endif
    assert(kernel_reads_of(&io.bi_next) == 0);

    /* Flush's final zero-byte C must not duplicate any samples. */
    struct latency_counters before = host[8].counters[0];
    kernel_read_count = 0;
    finish_request(&req, 0);
    assert(!kernel_read_count && !memcmp(&before, &host[8].counters[0], sizeof(before)));
}

int main(int argc, char **argv)
{
    assert(argc == 5);
    zero_extend_errno = !strcmp(argv[4], "zero-extended");
    request_has_disk = !strcmp(argv[3], "rq-disk");
    block_bio_queue_bio_arg = block_bio_remap_bio_arg =
        block_rq_remap_request_arg = block_split_bio_arg = atoi(argv[1]);
    /* Request buckets run in their own process, without sharing map state. */
    test_bucket_boundaries(!strcmp(argv[2], "completion-buckets"));
    if (!strcmp(argv[2], "completion-buckets"))
        return 0;
    if (!strcmp(argv[2], "request-time"))
        test_request_time();
    else if (!strcmp(argv[2], "host-only"))
        test_host_only();
    else if (!strcmp(argv[2], "root-blkcg"))
        test_root_blkcg(true);
    else if (!strcmp(argv[2], "root-blkcg-unknown"))
        test_root_blkcg(false);
    else if (!strcmp(argv[2], "unselected-disk"))
        test_unselected_disk();
    else if (!strcmp(argv[2], "completion-reads"))
        test_completion_reads_once();
    else if (!strcmp(argv[2], "single-completion"))
        test_single_bio_completion();
    else if (!strcmp(argv[2], "partial-completion"))
        test_partial_completion();
    else if (!strcmp(argv[2], "completion-chunks"))
        test_completion_chunks(false);
    else if (!strcmp(argv[2], "completion-chunks-partial"))
        test_completion_chunks(true);
    else if (!strcmp(argv[2], "remap"))
        test_remap();
    else if (!strcmp(argv[2], "partition-remap"))
        test_partition_remap();
    else if (!strcmp(argv[2], "split"))
        test_split();
    else if (!strcmp(argv[2], "clone"))
        test_clone_cleanup();
    else if (!strcmp(argv[2], "clone-delete"))
        test_clone_cleanup_error();
    else if (!strcmp(argv[2], "busy-state"))
        test_busy_state_writes();
    else if (!strcmp(argv[2], "busy-container"))
        test_busy_series_write();
    else if (!strcmp(argv[2], "container-retirement"))
        test_container_retirement();
    else if (!strcmp(argv[2], "container-allocation"))
        test_container_allocation_failure();
    else if (!strcmp(argv[2], "disk-retirement"))
        test_disk_retirement();
    else {
        assert(!strcmp(argv[2], "request"));
        test_request_device();
    }
    return 0;
}
`
	program = ioLatencyNativeC(program)
	compiler, err := exec.LookPath("cc")
	require.NoError(t, err, "a native C compiler is required")
	executable := filepath.Join(t.TempDir(), "iolatency-remap")
	compile := exec.CommandContext(t.Context(), compiler, "-x", "c", "-O2", "-o", executable, "-")
	compile.Stdin = strings.NewReader(program)
	output, err := compile.CombinedOutput()
	require.NoError(t, err, "%s", output)
	for _, slot := range []string{"0", "1"} {
		for _, scenario := range []string{"remap", "partition-remap", "request", "split", "clone", "clone-delete", "busy-state", "busy-container", "container-retirement", "container-allocation", "disk-retirement", "host-only", "root-blkcg", "root-blkcg-unknown", "request-time", "unselected-disk", "completion-reads", "single-completion", "partial-completion", "completion-chunks", "completion-chunks-partial", "completion-buckets"} {
			for _, layout := range []string{"rq-disk", "queue-disk"} {
				for _, errno := range []string{"sign-extended", "zero-extended"} {
					t.Run("slot-"+slot+"/"+scenario+"/"+layout+"/"+errno, func(t *testing.T) {
						output, err := exec.CommandContext(t.Context(), executable, slot, scenario, layout, errno).CombinedOutput()
						require.NoError(t, err, "%s", output)
					})
				}
			}
		}
	}
	t.Run("non-adjacent-bio-tail", func(t *testing.T) {
		compile := exec.CommandContext(t.Context(), compiler, "-x", "c", "-O2",
			"-DIO_LATENCY_TEST_BIO_TAIL_GAP", "-o", executable, "-")
		compile.Stdin = strings.NewReader(program)
		output, err := compile.CombinedOutput()
		require.NoError(t, err, "%s", output)
		output, err = exec.CommandContext(t.Context(), executable,
			"0", "single-completion", "rq-disk", "sign-extended").CombinedOutput()
		require.NoError(t, err, "%s", output)
	})
}

// GC: inactive ownership, candidate selection, and reclamation.

// Inactive-cache ownership, contention, and retirement.

func TestIOLatencyInactiveCache(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the test file")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "bpf", "iolatency_tracing.c"))
	if err != nil {
		t.Fatal(err)
	}
	cache, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "bpf", "include", "bpf_iolatency_cache.h"))
	if err != nil {
		t.Fatal(err)
	}
	gc, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "bpf", "iolatency_gc.c"))
	if err != nil {
		t.Fatal(err)
	}
	section := func(source []byte, start, end string) string {
		t.Helper()
		_, rest, found := strings.Cut(string(source), start)
		if !found {
			t.Fatalf("missing production section %s", start)
		}
		body, _, found := strings.Cut(rest, end)
		if !found {
			t.Fatalf("missing production section end %s", end)
		}
		return start + body
	}
	storage := section(cache, "static __always_inline u32 *io_latency_try_gate", "\n#endif") +
		section(data, "static __always_inline struct bio_latency_state *io_latency_begin_bio", "/* End inactive cache storage. */") +
		section(gc, "static __always_inline int io_latency_reclaim_bio", "/* The session loads")
	program := `
#include <assert.h>
#include <errno.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
typedef uint64_t u64;
typedef uint32_t u32;
#ifndef __always_inline
#define __always_inline inline __attribute__((always_inline))
#endif
#define __noinline __attribute__((noinline))
#define SEC(name)
#define COMPAT_BPF_ANY 0
#define COMPAT_BPF_NOEXIST 1
#define COMPAT_BPF_EXIST 2
#define REQ_OP_READ 0
#define REQ_OP_WRITE 1
#define READ_ONCE(value) (*(volatile __typeof__(value) *)&(value))
#define WRITE_ONCE(value, next) (*(volatile __typeof__(value) *)&(value) = (next))
#define barrier() asm volatile("" ::: "memory")
#define CORE_READ1(object, field) ({ __auto_type source = (object); source ? source->field : 0; })
#define CORE_READ2(object, first, second) CORE_READ1(CORE_READ1(object, first), second)
#define CORE_SELECT(_1, _2, _3, name, ...) name
#define BPF_CORE_READ(...) CORE_SELECT(__VA_ARGS__, CORE_READ2, CORE_READ1)(__VA_ARGS__)
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
` + section(data, "#define IO_LATENCY_BUCKETS", "#ifndef REQ_OP_MASK") +
		section(cache, "#define IO_LATENCY_BIO_STATES", "struct bio_latency_state {") +
		section(cache, "struct bio_latency_state {", "\n};") + "\n};\n" +
		section(data, "struct container_latency_key {", "\n};") + "\n};\n" + `
_Static_assert(sizeof(struct bio_latency_state) == 40, "state layout");
_Static_assert(IO_LATENCY_BIO_STATES == 10240, "state capacity");
static int bio_latency_map, bio_fallback_map;
struct entry {
    bool used;
    u64 key;
    struct bio_latency_state state;
};
struct fake_map {
    struct entry entries[IO_LATENCY_BIO_STATES];
    u32 updates, deletes;
    int update_error;
    int delete_error;
};
static struct fake_map primary, fallback;
static bool zero_extend_errno;
static u32 failure_count, failure_reason;
static int failure_error;
static void (*before_main_delete)(u64 key);
static void (*after_main_lookup)(u64 key);
static u64 clock_ns;

static struct fake_map *map_for(void *id)
{
    assert(id == &bio_latency_map || id == &bio_fallback_map);
    return id == &bio_latency_map ? &primary : &fallback;
}

static struct bio_latency_state *state_for(struct fake_map *map, u64 key)
{
    for (u32 i = 0; i < IO_LATENCY_BIO_STATES; i++)
        if (map->entries[i].used && map->entries[i].key == key)
            return &map->entries[i].state;
    return NULL;
}

static void seed(struct fake_map *map, u64 key, u64 queue_ns, u64 inactive_ns)
{
    assert(!state_for(map, key));
    for (u32 i = 0; i < IO_LATENCY_BIO_STATES; i++) {
        if (!map->entries[i].used) {
            map->entries[i] = (struct entry) {
                .used = true, .key = key,
                .state = { .queue_ns = queue_ns, .inactive_ns = inactive_ns,
                           .major = 8, .minor = 16, .blkcg = 7 },
            };
            return;
        }
    }
    abort();
}

static long map_result(int result)
{
    return zero_extend_errno ? (long)(u32)result : (long)result;
}

static void *bpf_map_lookup_elem(void *id, const void *key)
{
    struct bio_latency_state *state = state_for(map_for(id), *(const u64 *)key);
    if (id == &bio_latency_map && after_main_lookup) {
        void (*callback)(u64) = after_main_lookup;
        after_main_lookup = NULL;
        callback(*(const u64 *)key);
    }
    return state;
}

static long bpf_map_update_elem(void *id, const void *key,
                                const void *value, u64 flags)
{
    struct fake_map *map = map_for(id);
    u64 address = *(const u64 *)key;
    struct bio_latency_state *state = state_for(map, address);
    map->updates++;
    if (map->update_error)
        return map_result(map->update_error);
    if (state && flags == COMPAT_BPF_NOEXIST)
        return map_result(-EEXIST);
    if (!state && flags == COMPAT_BPF_EXIST)
        return map_result(-ENOENT);
    if (!state) {
        for (u32 i = 0; i < IO_LATENCY_BIO_STATES; i++) {
            if (!map->entries[i].used) {
                map->entries[i].used = true;
                map->entries[i].key = address;
                state = &map->entries[i].state;
                break;
            }
        }
    }
    if (!state)
        return map_result(-E2BIG);
    *state = *(const struct bio_latency_state *)value;
    return 0;
}

static long bpf_map_delete_elem(void *id, const void *key)
{
    struct fake_map *map = map_for(id);
    u64 address = *(const u64 *)key;
    map->deletes++;
    if (map->delete_error) {
        int error = map->delete_error;
        map->delete_error = 0;
        return map_result(error);
    }
    if (id == &bio_latency_map && before_main_delete) {
        void (*callback)(u64) = before_main_delete;
        before_main_delete = NULL;
        callback(address);
    }
    for (u32 i = 0; i < IO_LATENCY_BIO_STATES; i++) {
        if (map->entries[i].used && map->entries[i].key == address) {
            /* Keep the deleted value readable for the current RCU reader. */
            map->entries[i].used = false;
            return 0;
        }
    }
    return map_result(-ENOENT);
}

static void io_latency_fail(u32 reason, int error)
{
    failure_count++;
    failure_reason = reason;
    failure_error = error;
}

static u64 bpf_ktime_get_ns(void) { return clock_ns; }
` + storage + `
/* Kernel shapes needed by the real Q, C and clone entry points. */
struct blkcg_gq { void *blkcg; };
struct gendisk;
struct bio {
    struct { u64 bi_size; } bi_iter;
    struct blkcg_gq *bi_blkg;
    struct bio *bi_next;
};
struct request { struct bio *bio; };
struct pt_regs { void *arg1; };
/* Record completion accounting without reproducing the histogram buckets. */
struct latency_counters {
    u32 latency[3], size[2];
    u32 bucket[3];
    u64 start_ns[3], end_ns[3], bytes[2];
};
` + section(data, "struct host_latency_counters {", "\n};") + "\n};\n" + `
static struct host_latency_counters host_counters = { .major = 8, .minor = 16 };
static struct host_latency_counters *lookup_host_counters(struct gendisk *disk)
{ return &host_counters; }
static struct latency_counters counters, container_counters;
static struct container_latency_key last_container_key;
static struct fake_map *accounting_map;
static u64 accounting_key;
static bool io_latency_containers_enabled;
static u32 size_samples;
static bool reclaim_during_accounting;

static void assert_accounting_uses_retired_copy(void)
{
    if (accounting_map) {
        struct bio_latency_state *state = state_for(accounting_map, accounting_key);
        if (accounting_map == &primary) {
            assert(state && !state->queue_ns && state->inactive_ns);
            if (reclaim_during_accounting) {
                assert(io_latency_reclaim_bio(accounting_key, state->inactive_ns,
                                              state->inactive_ns) == 1);
                memset(state, 0, sizeof(*state));
                accounting_map = NULL;
            }
        } else {
            assert(!state);
        }
    }
}

static struct latency_counters *lookup_container_counters(const struct container_latency_key *key, struct gendisk *disk)
{ last_container_key = *key; return &container_counters; }
static void account_io_size(struct latency_counters *host,
                           struct latency_counters *container, u32 point, u64 bytes)
{
    assert_accounting_uses_retired_copy();
    assert(point < 2);
    if (host) { host->size[point]++; host->bytes[point] += bytes; }
    if (container) { container->size[point]++; container->bytes[point] += bytes; }
    size_samples++;
}

static void account_io_latency(struct latency_counters *host,
                              struct latency_counters *container,
                              u32 stage, u64 start_ns, u64 end_ns)
{
    assert_accounting_uses_retired_copy();
    assert(stage < 3);
    if (host) {
        host->latency[stage]++;
        host->start_ns[stage] = start_ns;
        host->end_ns[stage] = end_ns;
    }
    if (container) {
        container->latency[stage]++;
        container->start_ns[stage] = start_ns;
        container->end_ns[stage] = end_ns;
    }
}

static void increment_latency(struct latency_counters *counters, u32 stage, int bucket)
{
    assert_accounting_uses_retired_copy();
    assert(stage < 3 && (u32)bucket < IO_LATENCY_BUCKETS);
    counters->latency[stage]++;
    counters->bucket[stage] = bucket;
}
` + section(data, "static __noinline u32 queue_bio_latency(", "\n}\n") + "\n}\n" +
		section(data, "SEC(\"kprobe/blk_rq_unprep_clone\")", "\n}\n") + "\n}\n" +
		section(data, "struct request_completion {", "\n};") + "\n};\n" +
		section(data, "static __noinline bool read_completed_bio(", "\n}\n") + "\n}\n" +
		section(data, "static __noinline void complete_bio_latency(", "\n}\n") + "\n}\n" + `
static struct bio_latency_state next_state(u64 now)
{
    return (struct bio_latency_state) {
        .queue_ns = now, .blkcg = 11, .major = 259, .minor = 2,
    };
}

static void write_state(u64 key, u64 now)
{
    u32 *gate = NULL;
    bool temporary = false;
    struct bio_latency_state next = next_state(now);
    struct bio_latency_state *previous = io_latency_begin_bio(key, &gate, &temporary);
    io_latency_write_bio(key, &next, previous, gate, temporary);
}

static void assert_gates_released(void)
{
    for (u32 i = 0; i < IO_LATENCY_BIO_STATES; i++)
        assert(primary.entries[i].state.guard == 0);
}

static void test_inactive_reuse(void)
{
    const u64 key = 1;
    write_state(key, 100);
    struct bio_latency_state *first = state_for(&primary, key);
    assert(first && first->queue_ns == 100);
    assert(primary.updates == 1);
    io_latency_forget_bio(key, 200);
    assert(first->queue_ns == 0 && first->inactive_ns == 200);
    write_state(key, 300);
    struct bio_latency_state expected = next_state(300);
    assert(state_for(&primary, key) == first);
    assert(first->queue_ns == expected.queue_ns && first->blkcg == expected.blkcg);
    assert(first->major == expected.major && first->minor == expected.minor);
    assert(!first->guard && !first->deleted);
    assert(primary.updates == 1 && primary.deletes == 0);
    assert(!state_for(&fallback, key));
    assert(io_latency_reclaim_bio(key, 200, 400) == 0);
    io_latency_forget_bio(key, 400);
    assert(!first->queue_ns && first->inactive_ns == 400);
    assert(io_latency_reclaim_bio(key, 200, 400) == 0);
    assert(!failure_count);
    assert_gates_released();
}

static void test_active_fast_path(void)
{
    const u64 key = 2;
    write_state(key, 100);
    u32 *held = io_latency_try_gate(state_for(&primary, key));
    assert(held && *held == 1);
    u32 *gate = NULL;
    bool temporary = false;
    struct bio_latency_state *previous = io_latency_begin_bio(key, &gate, &temporary);
    assert(previous == state_for(&primary, key) && !gate && !temporary);
    struct bio_latency_state next = next_state(200);
    io_latency_write_bio(key, &next, previous, gate, temporary);
    assert(previous->queue_ns == 200 && *held == 1);
    io_latency_forget_bio(key, 300);
    assert(previous->queue_ns == 0 && previous->inactive_ns == 300);
    assert(*held == 1 && !state_for(&fallback, key));
    io_latency_release_gate(held);
    assert(!failure_count);
    assert_gates_released();
}

static void test_reclaim_conditions(void)
{
    seed(&primary, 10, 0, 10);
    seed(&primary, 20, 20, 0);
    seed(&primary, 30, 0, 90);
    seed(&primary, 40, 0, 10);
    assert(io_latency_reclaim_bio(20, 0, 100) == 0);
    assert(io_latency_reclaim_bio(30, 90, 50) == 0);
    assert(io_latency_reclaim_bio(999, 10, 100) == 0);
    write_state(40, 60);
    assert(io_latency_reclaim_bio(40, 10, 100) == 0);
    io_latency_forget_bio(40, 70);
    assert(io_latency_reclaim_bio(40, 10, 100) == 0);
    assert(state_for(&primary, 40)->inactive_ns == 70);
    assert(io_latency_reclaim_bio(10, 10, 10) == 1);
    assert(!state_for(&primary, 10));
    assert(state_for(&primary, 20)->queue_ns == 20);
    assert(state_for(&primary, 30)->inactive_ns == 90);
    assert(io_latency_reclaim_bio(40, 70, 100) == 1);
    assert(!failure_count);
    assert_gates_released();
}

static void test_gate_contention(void)
{
    const u64 key = 50;
    seed(&primary, key, 0, 10);
    u32 *held = io_latency_try_gate(state_for(&primary, key));
    assert(held && *held == 1);
    assert(!io_latency_try_gate(state_for(&primary, key)) && *held == 1);
    assert(io_latency_reclaim_bio(key, 10, 100) == -EBUSY && *held == 1);
    u32 *gate = NULL;
    bool temporary = false;
    struct bio_latency_state *previous = io_latency_begin_bio(key, &gate, &temporary);
    assert(!previous && !gate && temporary && *held == 1);
    struct bio_latency_state next = next_state(100);
    io_latency_write_bio(key, &next, previous, gate, temporary);
    assert(state_for(&fallback, key)->queue_ns == 100);
    assert(state_for(&primary, key)->queue_ns == 0);
    io_latency_release_gate(held);
    write_state(key, 200);
    assert(state_for(&fallback, key)->queue_ns == 200);
    assert(state_for(&primary, key)->queue_ns == 0);
    assert(state_for(&primary, key)->inactive_ns == 10);
    io_latency_forget_bio(key, 300);
    assert(!state_for(&fallback, key));
    assert(state_for(&primary, key)->inactive_ns == 10);
    assert(io_latency_reclaim_bio(key, 10, 100) == 1);
    assert(!failure_count);
    assert_gates_released();
}

static void activate_during_delete(u64 key)
{
    assert(state_for(&primary, key)->guard == 1);
    write_state(key, 200);
    assert(state_for(&primary, key)->queue_ns == 0);
    assert(state_for(&fallback, key)->queue_ns == 200);
    assert(state_for(&primary, key)->guard == 1);
}

/* Interrupt a Q/A lookup with the production reclaimer. The lookup still
 * returns its RCU-protected old pointer after the key has been unlinked.
 */
static void delete_during_lookup(u64 key)
{
    assert(io_latency_reclaim_bio(key, 10, 100) == 1);
}

static void test_deleted_pointer(void)
{
    const u64 key = 55;
    seed(&primary, key, 0, 10);
    struct bio_latency_state *old = state_for(&primary, key);
    after_main_lookup = delete_during_lookup;
    write_state(key, 200);
    assert(!after_main_lookup && !state_for(&primary, key));
    assert(!old->queue_ns && old->deleted && !old->guard);
    assert(state_for(&fallback, key)->queue_ns == 200);
    write_state(key, 300);
    assert(!state_for(&primary, key));
    assert(state_for(&fallback, key)->queue_ns == 300);
    io_latency_forget_bio(key, 400);
    assert(!state_for(&fallback, key) && !failure_count);
    assert_gates_released();
}

static void test_delete_overlaps_activation(void)
{
    const u64 key = 60;
    seed(&primary, key, 0, 10);
    before_main_delete = activate_during_delete;
    assert(io_latency_reclaim_bio(key, 10, 100) == 1);
    assert(!before_main_delete && !state_for(&primary, key));
    assert(state_for(&fallback, key)->queue_ns == 200);
    write_state(key, 300);
    assert(!state_for(&primary, key));
    assert(state_for(&fallback, key)->queue_ns == 300);
    io_latency_forget_bio(key, 400);
    assert(!state_for(&fallback, key));
    assert(!failure_count);
    assert_gates_released();
}

static void test_primary_full(bool inactive)
{
    for (u32 i = 0; i < IO_LATENCY_BIO_STATES; i++) {
        primary.entries[i] = (struct entry) {
            .used = true, .key = i + 1,
            .state = { .queue_ns = inactive ? 0 : 100, .inactive_ns = inactive ? 100 : 0 },
        };
    }
    /* New addresses keep using completion-owned fallback storage even
     * when no cache entry becomes eligible for GC during the workload.
     */
    for (u32 i = 0; i < 256; i++) {
        const u64 key = IO_LATENCY_BIO_STATES + i + 1;
        struct bio_latency_state completed;
        write_state(key, 200);
        assert(!state_for(&primary, key));
        assert(state_for(&fallback, key)->queue_ns == 200);
        assert(read_completed_bio(key, 300, &completed));
        assert(completed.queue_ns == 200);
        assert(!state_for(&fallback, key));
    }
    for (u32 i = 0; i < IO_LATENCY_BIO_STATES; i++) {
        assert(primary.entries[i].used);
        assert(primary.entries[i].state.queue_ns == (inactive ? 0 : 100));
    }
    assert(!failure_count);
    assert_gates_released();
}

static void test_cache_allocation_failure(void)
{
    const u64 key = 65;
    struct bio_latency_state completed;
    primary.update_error = -ENOMEM;
    write_state(key, 200);
    assert(!state_for(&primary, key));
    assert(!failure_count);
    assert(state_for(&fallback, key)->queue_ns == 200);
    assert(read_completed_bio(key, 300, &completed));
    assert(completed.queue_ns == 200);
    assert(!state_for(&fallback, key));
    assert(!failure_count);
    assert_gates_released();

    /* When neither table accepts the state, report the fallback error. */
    fallback.update_error = -E2BIG;
    write_state(key + 1, 400);
    assert(failure_count == 1);
    assert(failure_reason == IO_LATENCY_STATE_INSERT_FAILED);
    assert(failure_error == -E2BIG);
    assert(!state_for(&primary, key + 1));
    assert(!state_for(&fallback, key + 1));
    assert_gates_released();
}

static void test_delete_busy(void)
{
    const u64 key = 70;
    seed(&primary, key, 0, 10);
    primary.delete_error = -EBUSY;
    assert(io_latency_reclaim_bio(key, 10, 100) == -EBUSY);
    assert(state_for(&primary, key)->inactive_ns == 10 &&
           !state_for(&primary, key)->deleted);
    assert_gates_released();
    assert(io_latency_reclaim_bio(key, 10, 100) == 1);
    assert(!state_for(&primary, key));
    assert_gates_released();
}

static void test_clone_forget(void)
{
    struct bio tail = {}, head = { .bi_next = &tail };
    struct request req = { .bio = &head };
    struct pt_regs event = { .arg1 = &req };
    write_state((u64)&head, 100);
    seed(&primary, (u64)&tail, 0, 50);
    u32 *held = io_latency_try_gate(state_for(&primary, (u64)&tail));
    assert(held);
    write_state((u64)&tail, 100);
    io_latency_release_gate(held);
    assert(state_for(&fallback, (u64)&tail));
    clock_ns = 200;
    assert(kprobe_unprep_clone(&event) == 0);
    assert(state_for(&primary, (u64)&head)->queue_ns == 0);
    assert(state_for(&primary, (u64)&head)->inactive_ns == 200);
    assert(!state_for(&fallback, (u64)&tail));
    assert(!failure_count);
    assert_gates_released();
}

static void test_zero_byte_forget(void)
{
    struct bio io = {};
    const u64 key = (u64)&io;
    write_state(key, 100);
    assert(queue_bio_latency(&io, NULL, REQ_OP_READ, 200) == 0);
    assert(state_for(&primary, key)->queue_ns == 0);
    assert(state_for(&primary, key)->inactive_ns == 200);
    u32 *held = io_latency_try_gate(state_for(&primary, key));
    assert(held);
    write_state(key, 300);
    io_latency_release_gate(held);
    assert(state_for(&fallback, key));
    assert(queue_bio_latency(&io, NULL, REQ_OP_READ, 400) == 0);
    assert(!state_for(&fallback, key));
    assert(state_for(&primary, key)->inactive_ns == 200);
    assert(!size_samples && !failure_count);
    assert_gates_released();
}

static struct request_completion completion_for(u64 queue_ns)
{
    return (struct request_completion) {
        .key = { .major = 259, .minor = 2 }, .host = &counters,
        .get_request_ns = queue_ns + 10,
        .issue_ns = queue_ns + 20, .now = queue_ns + 30,
        .d2c_bucket = 0,
    };
}

static void assert_completion_counts(u32 count, u64 latest_queue_ns)
{
    for (u32 stage = 0; stage < 3; stage++) {
        assert(counters.latency[stage] == count);
        assert(container_counters.latency[stage] == count);
    }
    assert(counters.start_ns[IO_LATENCY_STAGE_Q2G] == latest_queue_ns);
    assert(counters.start_ns[IO_LATENCY_STAGE_Q2D] == latest_queue_ns);
    assert(counters.bucket[IO_LATENCY_STAGE_D2C] == 0);
    assert(container_counters.bucket[IO_LATENCY_STAGE_D2C] == 0);
    assert(counters.size[IO_SIZE_POINT_ISSUE] == count);
    assert(container_counters.size[IO_SIZE_POINT_ISSUE] == count);
    assert(counters.bytes[IO_SIZE_POINT_ISSUE] == (u64)count * 4096);
    assert(size_samples == count && last_container_key.blkcg == 11);
}

static void test_complete_primary(void)
{
    struct bio io = {};
    const u64 key = (u64)&io;
    struct request_completion completion = completion_for(100);
    write_state(key, 100);
    accounting_map = &primary;
    accounting_key = key;
    complete_bio_latency(&io, &completion, 4096);
    assert_completion_counts(1, 100);
    assert(state_for(&primary, key)->queue_ns == 0);
    assert(state_for(&primary, key)->inactive_ns == completion.now);
    assert(!primary.deletes && !state_for(&fallback, key));
    complete_bio_latency(&io, &completion, 4096);
    assert_completion_counts(1, 100);
    assert(!failure_count);
    assert_gates_released();
}

static void test_complete_fallback(void)
{
    struct bio io = {};
    const u64 key = (u64)&io;
    seed(&primary, key, 0, 10);
    u32 *held = io_latency_try_gate(state_for(&primary, key));
    assert(held);
    write_state(key, 100);
    io_latency_release_gate(held);
    write_state(key, 200);
    accounting_map = &fallback;
    accounting_key = key;
    struct request_completion completion = completion_for(200);
    complete_bio_latency(&io, &completion, 4096);
    assert_completion_counts(1, 200);
    assert(!state_for(&fallback, key));
    assert(state_for(&primary, key)->queue_ns == 0);
    assert(state_for(&primary, key)->inactive_ns == 10);

    /* GC creates a second fallback cycle through the real helper callback. */
    before_main_delete = activate_during_delete;
    assert(io_latency_reclaim_bio(key, 10, 50) == 1);
    assert(!state_for(&primary, key));
    write_state(key, 300);
    assert(state_for(&fallback, key)->queue_ns == 300);
    assert(!state_for(&primary, key));
    completion = completion_for(300);
    complete_bio_latency(&io, &completion, 4096);
    assert_completion_counts(2, 300);
    assert(!state_for(&fallback, key));
    complete_bio_latency(&io, &completion, 4096);
    assert_completion_counts(2, 300);
    assert(!failure_count);
    assert_gates_released();
}

static void test_complete_inactive(void)
{
    struct bio io = {}, missing = {};
    const u64 key = (u64)&io;
    seed(&primary, key, 0, 10);
    accounting_map = &primary;
    accounting_key = key;
    struct request_completion completion = completion_for(100);
    complete_bio_latency(&io, &completion, 4096);
    complete_bio_latency(&missing, &completion, 4096);
    for (u32 stage = 0; stage < 3; stage++) {
        assert(!counters.latency[stage]);
        assert(!container_counters.latency[stage]);
    }
    assert(!size_samples && !failure_count);
    assert(state_for(&primary, key)->inactive_ns == 10);
    assert(!primary.deletes && !fallback.deletes);
    assert_gates_released();
}

static void test_reclaim_during_accounting(void)
{
    struct bio io = {};
    const u64 key = (u64)&io;
    write_state(key, 100);
    accounting_map = &primary;
    accounting_key = key;
    reclaim_during_accounting = true;
    struct request_completion completion = completion_for(100);
    complete_bio_latency(&io, &completion, 4096);
    assert_completion_counts(1, 100);
    assert(!state_for(&primary, key) && !state_for(&fallback, key));
    write_state(key, 200);
    completion = completion_for(200);
    complete_bio_latency(&io, &completion, 4096);
    assert_completion_counts(2, 200);
    assert(!failure_count);
    assert_gates_released();
}

int main(int argc, char **argv)
{
    assert(argc == 3);
    zero_extend_errno = !strcmp(argv[2], "zero-extended");
    if (!strcmp(argv[1], "inactive-reuse")) test_inactive_reuse();
    else if (!strcmp(argv[1], "active-fast-path")) test_active_fast_path();
    else if (!strcmp(argv[1], "reclaim-conditions")) test_reclaim_conditions();
    else if (!strcmp(argv[1], "gate-contention")) test_gate_contention();
    else if (!strcmp(argv[1], "deleted-pointer")) test_deleted_pointer();
    else if (!strcmp(argv[1], "delete-overlaps-activation")) test_delete_overlaps_activation();
    else if (!strcmp(argv[1], "primary-full")) test_primary_full(false);
    else if (!strcmp(argv[1], "inactive-cache-full")) test_primary_full(true);
    else if (!strcmp(argv[1], "cache-allocation-failure")) test_cache_allocation_failure();
    else if (!strcmp(argv[1], "delete-busy")) test_delete_busy();
    else if (!strcmp(argv[1], "clone-forget")) test_clone_forget();
    else if (!strcmp(argv[1], "reclaim-during-accounting")) test_reclaim_during_accounting();
    else if (!strcmp(argv[1], "complete-primary")) test_complete_primary();
    else if (!strcmp(argv[1], "complete-fallback")) test_complete_fallback();
    else if (!strcmp(argv[1], "complete-inactive")) test_complete_inactive();
    else { assert(!strcmp(argv[1], "zero-byte-forget")); test_zero_byte_forget(); }
    return 0;
}
`
	program = ioLatencyNativeC(program)
	compiler, err := exec.LookPath("cc")
	if err != nil {
		t.Fatal("a native C compiler is required: ", err)
	}
	executable := filepath.Join(t.TempDir(), "iolatency-inactive-gc")
	compile := exec.CommandContext(t.Context(), compiler, "-x", "c", "-std=gnu11", "-O2", "-o", executable, "-")
	compile.Stdin = strings.NewReader(program)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile production storage: %v\n%s", err, output)
	}
	for _, scenario := range []string{
		"inactive-reuse", "active-fast-path", "reclaim-conditions", "gate-contention", "deleted-pointer",
		"delete-overlaps-activation", "primary-full", "delete-busy", "clone-forget", "zero-byte-forget",
		"inactive-cache-full", "cache-allocation-failure",
		"complete-primary", "complete-fallback", "complete-inactive", "reclaim-during-accounting",
	} {
		for _, errno := range []string{"sign-extended", "zero-extended"} {
			t.Run(scenario+"/"+errno, func(t *testing.T) {
				if output, err := exec.CommandContext(t.Context(), executable, scenario, errno).CombinedOutput(); err != nil {
					t.Fatalf("%v\n%s", err, output)
				}
			})
		}
	}
}

// Native execution keeps the production branch graph. Live object loading
// checks the BPF instructions that bound older verifier exploration.
func ioLatencyNativeC(program string) string {
	return strings.NewReplacer(
		`asm goto("if %0 != 0 goto %l[queued]" : : "r"(state) : : queued);`,
		`if (state) goto queued;`,
		`asm goto("if %0 != 0 goto %l[temporary_queued]" : : "r"(state) : : temporary_queued);`,
		`if (state) goto temporary_queued;`,
		`asm goto("if %0 != 0 goto %l[active]" : : "r"(queue_ns) : : active);`,
		`if (queue_ns) goto active;`,
		`asm goto("if %0 != 0 goto %l[active]" : : "r"(found) : : active);`,
		`if (found) goto active;`,
		`asm goto("if %0 != 0 goto %l[temporary_active]" : : "r"(queue_ns) : : temporary_active);`,
		`if (queue_ns) goto temporary_active;`,
	).Replace(program)
}

// GC selection, cancellation, and kernel reclamation.

type ioLatencyGCIterator struct {
	states []ioLatencyBioState
	next   int
	err    error
}

func (i *ioLatencyGCIterator) Next(key, value any) bool {
	if i.next == len(i.states) {
		return false
	}
	*key.(*uint64) = uint64(i.next + 1)
	*value.(*ioLatencyBioState) = i.states[i.next]
	i.next++
	return true
}

func (i *ioLatencyGCIterator) Err() error { return i.err }

func TestIOLatencyGCSelectsIdleEntriesBeforeDeletion(t *testing.T) {
	iterator := &ioLatencyGCIterator{states: []ioLatencyBioState{
		{QueueNS: 1, InactiveNS: 1}, // active, even with an old idle timestamp
		{},                          // never published inactive
		{InactiveNS: 10},
		{InactiveNS: 11},
		{InactiveNS: 20},
	}}
	var jobs []ioLatencyGCJob
	err := reclaimIOLatencyBios(context.Background(), iterator, 11,
		func(job ioLatencyGCJob) error {
			require.Equal(t, len(iterator.states), iterator.next)
			jobs = append(jobs, job)
			return nil
		})
	require.NoError(t, err)
	require.Equal(t, []ioLatencyGCJob{
		{Bio: 3, InactiveNS: 10, CutoffNS: 11},
		{Bio: 4, InactiveNS: 11, CutoffNS: 11},
	}, jobs)
}

func TestIOLatencyGCCancellationAndErrors(t *testing.T) {
	t.Run("cancel during reclaim", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		iterator := &ioLatencyGCIterator{states: []ioLatencyBioState{
			{InactiveNS: 1}, {InactiveNS: 2},
		}}
		var jobs []ioLatencyGCJob
		err := reclaimIOLatencyBios(ctx, iterator, 10, func(job ioLatencyGCJob) error {
			jobs = append(jobs, job)
			cancel()
			return nil
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Len(t, jobs, 1)
	})
	t.Run("scan error does not start deletion", func(t *testing.T) {
		failure := errors.New("map read failed")
		iterator := &ioLatencyGCIterator{
			states: []ioLatencyBioState{{InactiveNS: 1}}, err: failure,
		}
		err := reclaimIOLatencyBios(context.Background(), iterator, 10,
			func(ioLatencyGCJob) error {
				t.Fatal("deleted from a failed scan")
				return nil
			})
		require.ErrorIs(t, err, failure)
	})
	t.Run("reclaim error", func(t *testing.T) {
		failure := errors.New("program run failed")
		iterator := &ioLatencyGCIterator{states: []ioLatencyBioState{{InactiveNS: 1}}}
		err := reclaimIOLatencyBios(context.Background(), iterator, 10,
			func(ioLatencyGCJob) error { return failure })
		require.ErrorIs(t, err, failure)
	})
}

func TestIOLatencyGCProcessesAFullCache(t *testing.T) {
	iterator := &ioLatencyGCIterator{states: make([]ioLatencyBioState, ioLatencyBioStates)}
	for i := range iterator.states {
		iterator.states[i].InactiveNS = 1
	}
	count := 0
	err := reclaimIOLatencyBios(context.Background(), iterator, 10,
		func(ioLatencyGCJob) error { count++; return nil })
	require.NoError(t, err)
	require.Equal(t, ioLatencyBioStates, count)
}

// The object stays unattached: test-run invokes the production reclaimer and
// uses its real cache map without observing unrelated host IO.
func TestIOLatencyGCKernel(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to run the bio cache reclaimer")
	}
	require.NoError(t, bpf.Init(&bpf.Option{}))
	t.Cleanup(bpf.Shutdown)
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	oldDir := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join(filepath.Dir(file), "..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = oldDir })
	object, err := bpf.LoadBPF("iolatency_tracing.o", nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, object.Close()) })
	gc, err := newIOLatencyGC(object)
	require.NoError(t, err)
	t.Cleanup(gc.close)
	require.Len(t, gc.object.Maps, 1)
	cache := gc.object.Maps["bio_latency_map"]
	// Full-width addresses and timestamps travel through the production
	// test-run payload; both freshness conditions must survive decoding.
	const keyBase = uint64(0xffff888000000000)
	const idleNS = uint64(1<<40 + 10)
	active := ioLatencyBioState{QueueNS: 1, InactiveNS: idleNS}
	idle := ioLatencyBioState{InactiveNS: idleNS}
	newer := ioLatencyBioState{InactiveNS: idleNS + 2}
	require.NoError(t, cache.Put(keyBase+1, active))
	require.NoError(t, cache.Put(keyBase+2, idle))
	require.NoError(t, cache.Put(keyBase+3, newer))
	var state ioLatencyBioState
	// A matching timestamp alone is insufficient when the entry is too young.
	require.NoError(t, gc.reclaim(ioLatencyGCJob{Bio: keyBase + 2, InactiveNS: idleNS, CutoffNS: idleNS - 1}))
	require.NoError(t, cache.Lookup(keyBase+2, &state))
	require.Equal(t, idle, state)
	// An old enough entry also survives if it has changed since selection.
	require.NoError(t, gc.reclaim(ioLatencyGCJob{Bio: keyBase + 2, InactiveNS: idleNS - 1, CutoffNS: idleNS + 1}))
	require.NoError(t, cache.Lookup(keyBase+2, &state))
	require.Equal(t, idle, state)
	for _, key := range []uint64{keyBase + 1, keyBase + 2, keyBase + 3} {
		require.NoError(t, gc.reclaim(ioLatencyGCJob{Bio: key, InactiveNS: idleNS, CutoffNS: idleNS + 1}))
	}
	require.NoError(t, cache.Lookup(keyBase+1, &state))
	require.Equal(t, active, state)
	require.ErrorIs(t, cache.Lookup(keyBase+2, &state), ebpf.ErrKeyNotExist)
	require.NoError(t, cache.Lookup(keyBase+3, &state))
	require.Equal(t, newer, state)

	// A packet without the job is rejected before any cache access.
	packet := [34]byte{12: 0x08, 14: 0x45, 17: 20}
	result, err := gc.object.Programs["bio_gc_run"].Run(&ebpf.RunOptions{Data: packet[:], Repeat: 1})
	require.NoError(t, err)
	require.Equal(t, -int32(unix.EFAULT), int32(result))
	require.NoError(t, cache.Lookup(keyBase+3, &state))
	require.Equal(t, newer, state)

	// Exercise candidate selection and BPF deletion together; active state
	// survives both the snapshot and the deletion recheck.
	require.NoError(t, reclaimIOLatencyBios(context.Background(), cache.Iterate(), idleNS+2, gc.reclaim))
	require.NoError(t, cache.Lookup(keyBase+1, &state))
	require.ErrorIs(t, cache.Lookup(keyBase+3, &state), ebpf.ErrKeyNotExist)

	// The periodic sweep keeps recent completions and active IO, while
	// reclaiming entries that have been inactive for more than one minute.
	var now unix.Timespec
	require.NoError(t, unix.ClockGettime(unix.CLOCK_MONOTONIC, &now))
	require.Greater(t, now.Nano(), int64(2*time.Minute))
	recent := ioLatencyBioState{InactiveNS: uint64(now.Nano() - int64(30*time.Second))}
	expired := ioLatencyBioState{InactiveNS: uint64(now.Nano() - int64(2*time.Minute))}
	require.NoError(t, cache.Put(keyBase+4, recent))
	require.NoError(t, cache.Put(keyBase+5, expired))
	require.NoError(t, gc.sweep(context.Background()))
	require.NoError(t, cache.Lookup(keyBase+1, &state))
	require.Equal(t, active, state)
	require.NoError(t, cache.Lookup(keyBase+4, &state))
	require.Equal(t, recent, state)
	require.ErrorIs(t, cache.Lookup(keyBase+5, &state), ebpf.ErrKeyNotExist)
}

// Qualification: kernel prerequisites and real IO.

// Startup admission and hash-allocator qualification.

func TestIOLatencyKernelStartup(t *testing.T) {
	for _, test := range []struct {
		name        string
		symbols     string
		config      string
		want        string
		root        uint64
		statDisable bool
	}{
		{
			name:   "fallback not configured",
			config: "CONFIG_BLOCK=y\n# CONFIG_BLK_INLINE_ENCRYPTION is not set\n",
		},
		{
			name:   "fallback disabled",
			config: "CONFIG_BLK_INLINE_ENCRYPTION=y\n# CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK is not set\n",
		},
		{
			name:        "accounting disable notification follows symbol presence",
			symbols:     "ffffffff81001000 T blk_stat_disable_accounting\n",
			config:      "CONFIG_BLOCK=y\n",
			statDisable: true,
		},
		{
			name:    "root blkcg address is a startup constant",
			symbols: "ffffffff82003000 B blkcg_root\n",
			config:  "CONFIG_BLOCK=y\n",
			root:    0xffffffff82003000,
		},
		{
			name: "startup without driver symbols", symbols: "ffffffff81001000 T unrelated\n",
			config: "CONFIG_BLOCK=y\n",
		},
		{
			name:   "enabled fallback requires its data symbol",
			config: "CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK=y\n",
			want:   "tfms_inited",
		},
		{
			name: "unavailable configuration is not disabled fallback",
			want: "kernel config",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			previous := filepath.Dir(procfs.DefaultPath())
			procfs.RootPrefix(root)
			t.Cleanup(func() { procfs.RootPrefix(previous) })
			word := &btf.Int{Name: "unsigned long", Size: 8, Encoding: btf.Unsigned}
			// An unrelated allocator type does not mean HASH uses it. These
			// fixtures have no FUNC records: only structure metadata is needed.
			writeIOLatencyKernelBTF(t, []btf.Type{
				ioControlHashBTFFixture(),
				&btf.Struct{Name: "bpf_mem_alloc", Size: 8, Members: []btf.Member{{Name: "cache", Type: word}}},
			})
			if err := os.MkdirAll(procfs.DefaultPath(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(procfs.Path("kallsyms"), []byte(test.symbols), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.config != "" {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				if _, err := writer.Write([]byte(test.config)); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(procfs.Path("config.gz"), compressed.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			constants, statDisable, err := loadIOLatencyKernelConstants()
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("startup error = %v; want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if constants["io_latency_root_blkcg"] != test.root {
				t.Fatalf("root blkcg = %v; want %#x", constants["io_latency_root_blkcg"], test.root)
			}
			if statDisable != test.statDisable {
				t.Fatalf("accounting disable hook = %t, want %t", statDisable, test.statDisable)
			}
		})
	}
}

// Feed real BTF bytes through Start so unsupported or unknown allocators stop
// before symbol discovery, BPF loading, or session publication.
func TestIOLatencyHashAllocatorStartup(t *testing.T) {
	word := &btf.Int{Name: "unsigned long", Size: 8, Encoding: btf.Unsigned}
	allocator := &btf.Struct{
		Name: "bpf_mem_alloc", Size: 8,
		Members: []btf.Member{{Name: "cache", Type: word}},
	}
	for _, test := range []struct {
		name      string
		types     []btf.Type
		missing   bool
		malformed bool
		want      string
		cause     error
	}{
		{
			name:  "hash uses the BPF allocator",
			types: []btf.Type{ioControlHashBTFFixture(btf.Member{Name: "ma", Type: allocator})},
			want:  "bpf_htab.ma uses bpf_mem_alloc",
		},
		{
			name: "allocator detected through type qualifiers",
			types: []btf.Type{ioControlHashBTFFixture(btf.Member{Name: "allocator", Type: &btf.Typedef{
				Name: "allocator_type", Type: &btf.Const{Type: allocator},
			}})},
			want: "bpf_htab.allocator uses bpf_mem_alloc",
		},
		{
			name: "missing hash type", types: []btf.Type{word},
			want: "resolve struct bpf_htab", cause: btf.ErrNotFound,
		},
		{
			name:  "incomplete hash type",
			types: []btf.Type{&btf.Struct{Name: "bpf_htab"}},
			want:  "resolve ordinary HASH bpf_htab", cause: btf.ErrNotFound,
		},
		{name: "missing BTF", missing: true, want: "sys/kernel/btf/vmlinux", cause: os.ErrNotExist},
		{name: "malformed BTF", malformed: true, want: "sys/kernel/btf/vmlinux"},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := filepath.Dir(procfs.DefaultPath())
			procfs.RootPrefix(t.TempDir())
			t.Cleanup(func() { procfs.RootPrefix(previous) })
			if !test.missing {
				writeIOLatencyKernelBTF(t, test.types)
				if test.malformed {
					path := filepath.Join(procfs.DefaultPathByType("sys"), "kernel", "btf", "vmlinux")
					if err := os.WriteFile(path, []byte("invalid BTF"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			collector := &iolatencyTracing{}
			err := collector.Start(context.Background())
			if !errors.Is(err, types.ErrTracingStopped) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("startup error = %v, want stopped with %q", err, test.want)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("startup error = %v, want underlying cause %v", err, test.cause)
			}
			if collector.session != nil {
				t.Fatal("unsupported allocator published a session")
			}
		})
	}
}

// Ordinary HASH and socket HASH may have the same BTF name in one kernel.
func TestIOLatencyHashAllocatorSameName(t *testing.T) {
	word := &btf.Int{Name: "unsigned long", Size: 8}
	hash := ioControlHashBTFFixture()
	socket := &btf.Struct{Name: "bpf_htab", Size: 8, Members: []btf.Member{{Name: "progs", Type: word}}}
	otherHash := ioControlHashBTFFixture(btf.Member{Name: "count", Type: word})
	allocatorHash := ioControlHashBTFFixture(btf.Member{Name: "ma", Type: &btf.Struct{Name: "bpf_mem_alloc", Size: 8}})
	for _, candidates := range [][]btf.Type{
		{hash, socket},
		{socket, hash},
		{hash, otherHash, socket},
		{socket, otherHash, hash},
		{hash, allocatorHash},
		{allocatorHash, hash},
	} {
		builder, err := btf.NewBuilder(candidates)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := builder.Marshal(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		err = checkIOLatencyHashAllocator(spec)
		unsupported := candidates[0] == allocatorHash || candidates[1] == allocatorHash
		if unsupported {
			if !errors.Is(err, types.ErrTracingStopped) || !strings.Contains(err.Error(), "uses bpf_mem_alloc") {
				t.Fatalf("allocator conflict = %v, want unsupported allocator", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func writeIOLatencyKernelBTF(t *testing.T, kernelTypes []btf.Type) {
	t.Helper()
	builder, err := btf.NewBuilder(kernelTypes)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := builder.Marshal(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(procfs.DefaultPathByType("sys"), "kernel", "btf", "vmlinux")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Software blk-crypto prerequisite decoding.

func TestIOLatencyCryptoModeCount(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []btf.EnumValue
		want   uint32
	}{
		{name: "four modes", values: []btf.EnumValue{{Name: "BLK_ENCRYPTION_MODE_MAX", Value: 4}}, want: 4},
		{name: "five modes", values: []btf.EnumValue{{Name: "BLK_ENCRYPTION_MODE_MAX", Value: 5}}, want: 5},
		{name: "missing bound", values: []btf.EnumValue{{Name: "BLK_ENCRYPTION_MODE_INVALID", Value: 0}}},
		{name: "empty array", values: []btf.EnumValue{{Name: "BLK_ENCRYPTION_MODE_MAX", Value: 0}}},
		{name: "array exceeds probe capacity", values: []btf.EnumValue{{Name: "BLK_ENCRYPTION_MODE_MAX", Value: 65}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder, err := btf.NewBuilder([]btf.Type{&btf.Enum{
				Name: "blk_crypto_mode_num", Size: 4, Values: test.values,
			}})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := builder.Marshal(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			count, err := ioLatencyCryptoModeCount(spec)
			if test.want == 0 {
				if err == nil {
					t.Fatal("invalid array bound accepted")
				}
			} else if err != nil || count != test.want {
				t.Fatalf("mode count = %d, %v; want %d", count, err, test.want)
			}
		})
	}
}

// Only the array bound is consumed; equivalent enums from different
// compilation units may differ in unrelated values.
func TestIOLatencyCryptoRepeatedEnums(t *testing.T) {
	for _, bound := range []uint64{4, 5} {
		for _, first := range []bool{false, true} {
			primary := &btf.Enum{
				Name: "blk_crypto_mode_num", Size: 4,
				Values: []btf.EnumValue{{Name: "BLK_ENCRYPTION_MODE_MAX", Value: 4}},
			}
			duplicate := &btf.Enum{
				Name: "blk_crypto_mode_num", Size: 4,
				Values: []btf.EnumValue{{Name: "BLK_ENCRYPTION_MODE_MAX", Value: bound}, {Name: "other", Value: 0}},
			}
			candidates := []btf.Type{primary, duplicate}
			if first {
				candidates[0], candidates[1] = candidates[1], candidates[0]
			}
			builder, err := btf.NewBuilder(candidates)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := builder.Marshal(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			count, err := ioLatencyCryptoModeCount(spec)
			if bound == 4 {
				if err != nil || count != 4 {
					t.Fatalf("equivalent enums = %d, %v; want 4", count, err)
				}
			} else if err == nil {
				t.Fatal("conflicting enum bounds accepted")
			}
		}
	}
}

type fakeIOLatencyCryptoBPF struct {
	bpf.BPF
	result    int64
	attachErr error
	readErr   error
}

// Queue timestamps and disk enrollment isolation.

// A successful startup requires both kernel timestamps on every target disk.
// The BPF result is supplied at the map-read boundary, after reading iostats.
// Hidden NVMe physical paths have no sysfs dev file; identity comes from BPF.
func TestIOLatencyQueueStats(t *testing.T) {
	for _, test := range []struct {
		name    string
		disk    string
		iostats string
		result  int64
		want    string
		skip    bool
	}{
		{name: "hidden NVMe path with both timestamps enabled", iostats: "1\n", result: 2},
		{name: "NVMe namespace", disk: "nvme0n1", iostats: "1\n", result: 2},
		{name: "NVMe multipath head without timestamps", disk: "nvme0n1", iostats: "0\n", result: 1},
		{name: "SCSI disk", disk: "sda", iostats: "1\n", result: 2},
		{name: "SCSI extended disk name", disk: "sdaa", iostats: "1\n", result: 2},
		{name: "G disabled", iostats: "0\n", result: 2, want: "iostats"},
		{name: "registered disk G disabled", iostats: "0\n", result: 5, want: "iostats"},
		{name: "D disabled", iostats: "1\n", result: 3, want: "request time statistics"},
		{name: "probe missed", iostats: "1\n", want: "did not execute"},
		{name: "failed kernel read", iostats: "1\n", result: -int64(unix.EFAULT), want: "bad address"},
		{name: "device mapper is not probed", disk: "dm-0", skip: true},
		{name: "MD is not probed", disk: "md0", skip: true},
		{name: "loop is not probed", disk: "loop0", skip: true},
		{name: "virtio is not probed", disk: "vda", skip: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			disk := test.disk
			if disk == "" {
				disk = "nvme0c1n1"
			}
			path := filepath.Join(root, "block", disk)
			if err := os.MkdirAll(filepath.Join(path, "queue"), 0o700); err != nil {
				t.Fatal(err)
			}
			if !test.skip {
				if err := os.WriteFile(filepath.Join(path, "queue", "iostats"), []byte(test.iostats), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			object := &fakeIOLatencyQueueBPF{result: test.result}
			session := &ioLatencySession{object: object}
			disks, err := session.discoverDisks(root, true)
			if test.skip {
				if err != nil || len(disks) != 0 || len(object.requests) != 0 {
					t.Fatalf("excluded disk: disks=%v, requests=%v, error=%v", disks, object.requests, err)
				}
				return
			}
			if string(bytes.TrimRight(object.keys[0], "\x00")) != disk {
				t.Fatalf("probe requested disk %q", object.keys[0])
			}
			if object.armed || session.diskProbeName != [32]byte{} {
				t.Fatal("discovery left its publication command armed")
			}
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error = %v, want %q", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.result == 1 {
				if len(disks) != 0 || len(object.requests) != 1 || len(session.retiredDisks) != 0 {
					t.Fatalf("skipped disk: disks=%v, requests=%v, retired=%v", disks, object.requests, session.retiredDisks)
				}
				return
			}
			if len(disks) != 1 || disks[0].Major != 259 || disks[0].Minor != 7 ||
				disks[0].Disk != 0xffff888012340000 {
				t.Fatalf("disks = %v", disks)
			} else if len(object.requests) != 2 || object.requests[1].Publish != 1 ||
				object.requests[1].Disk != disks[0].Disk || len(session.retiredDisks) != 0 {
				t.Fatalf("registration commands = %v", object.requests)
			}
		})
	}
}

// Execute the production registration probe with kernel reads and map helpers
// supplied by a native fixture. Only the requested disk's iostats read may
// publish counters; repeated reads preserve existing counters.
func TestIOLatencyQueueProbeNVMeHead(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return test file")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "bpf", "include", "bpf_iolatency_disk.h"))
	if err != nil {
		t.Fatal(err)
	}
	section := func(start, end string) string {
		t.Helper()
		_, rest, found := strings.Cut(string(data), start)
		if !found {
			t.Fatalf("missing production section %s", start)
		}
		body, _, found := strings.Cut(rest, end)
		if !found {
			t.Fatalf("missing production section end %s", end)
		}
		return start + body
	}
	program := `
#include <assert.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
typedef uint32_t __u32;
typedef uint64_t __u64;
typedef int64_t __s64;
#define SEC(name)
#define PT_REGS_PARM1_CORE(ctx) ((ctx)->arg1)
#define PT_REGS_PARM2_CORE(ctx) ((ctx)->arg2)
#define bpf_core_field_exists(field) stats_has_accounting
#define blk_queue_stats___queue_probe blk_queue_stats
#define BPF_CORE_READ_INTO(dst, src, field) \
    (memcpy((dst), &(src)->field, sizeof(*(dst))), 0)
#define QUEUE_PROBE_CONTAINER(ptr, type, field) \
    ((type *)((char *)(ptr) - offsetof(type, field)))
#define IO_LATENCY_EFAULT 14
#define IO_LATENCY_EEXIST 17
#define COMPAT_BPF_NOEXIST 1
struct pt_regs { __u64 arg1, arg2; };
struct attribute { const char *name; };
struct queue_probe_key { char name[32]; };
struct kobject { struct kobject *parent; const char *name; };
struct device { struct kobject kobj; struct gendisk *disk; };
struct list_head { struct list_head *next; };
struct blk_mq_ops { int unused; };
struct blk_queue_stats {
    struct list_head callbacks;
    int accounting;
    bool enable_accounting;
};
struct request_queue {
    struct blk_queue_stats *stats;
    const struct blk_mq_ops *mq_ops;
};
struct gendisk {
    struct request_queue *queue;
    __u32 major, first_minor;
};
struct host_latency_counters { __u32 major, minor; __u64 retired, samples; };
struct disk_entry { __u64 disk; __u32 major, minor; };
` + section("struct queue_probe_result {", "\nstruct {") + `
static struct queue_probe_result command;
static struct queue_probe_key target;
static int io_latency_queue_probe, blkdisk_lat_map, blkdisk_map;
static unsigned updates;
static bool registered, freeze_registered, duplicate_reader;
static bool stats_has_accounting;
static struct host_latency_counters host;
static struct disk_entry freeze;
static struct pt_regs duplicate_ctx;
static int probe_queue_stats(struct pt_regs *ctx);
static struct gendisk *io_latency_device_disk(struct device *device)
{
    return device->disk;
}
static int bpf_probe_read_str(char *dst, unsigned size, const char *src)
{
    unsigned length = strlen(src) + 1;
    if (length > size)
        length = size;
    memcpy(dst, src, length);
    dst[length - 1] = 0;
    return length;
}
static void *bpf_map_lookup_elem(void *map, const void *key)
{
    if (map == &io_latency_queue_probe)
        return memcmp(key, &target, sizeof(target)) ? NULL : &command;
    if (map == &blkdisk_lat_map)
        return registered ? &host : NULL;
    assert(map == &blkdisk_map);
    return freeze_registered ? &freeze : NULL;
}
static long bpf_map_update_elem(void *map, const void *key,
                               const void *value, __u64 flags)
{
    assert(map == &blkdisk_lat_map || map == &blkdisk_map);
    assert(flags == COMPAT_BPF_NOEXIST);
    updates++;
    if (map == &blkdisk_lat_map) {
        if (registered)
            return -IO_LATENCY_EEXIST;
        host = *(const struct host_latency_counters *)value;
        registered = true;
        if (duplicate_reader) {
            duplicate_reader = false;
            assert(!probe_queue_stats(&duplicate_ctx));
            assert(freeze_registered);
        }
    } else {
        if (freeze_registered)
            return -IO_LATENCY_EEXIST;
        freeze = *(const struct disk_entry *)value;
        freeze_registered = true;
    }
    return 0;
}
` + section("SEC(\"kprobe/queue_attr_show\")", "\nstruct {") + `
int main(int argc, char **argv)
{
    assert(argc == 8);
    stats_has_accounting = !strcmp(argv[7], "accounting");
    struct blk_queue_stats stats = {
        .callbacks.next = &stats.callbacks,
        .accounting = !strcmp(argv[3], "enabled"),
        .enable_accounting = !strcmp(argv[3], "enabled"),
    };
    struct blk_mq_ops ops = {};
    struct request_queue queue = {
        .stats = &stats,
        .mq_ops = !strcmp(argv[2], "mq") ? &ops : NULL,
    };
    struct gendisk disk = { .queue = &queue, .major = 259, .first_minor = 7 };
    struct device disk_device = { .kobj.name = argv[1], .disk = &disk };
    struct kobject kobj = { .parent = &disk_device.kobj };
    struct attribute attr = { .name = argv[5] };
    struct pt_regs ctx = { .arg1 = (__u64)&kobj, .arg2 = (__u64)&attr };
    strcpy(target.name, argv[6]);
    command.publish = strcmp(argv[4], "probe") != 0;
    duplicate_reader = !strcmp(argv[4], "duplicate");
    duplicate_ctx = ctx;
    command.disk = (__u64)&disk;
    command.initial.major = disk.major;
    command.initial.minor = disk.first_minor;
    assert(!probe_queue_stats(&ctx));
    assert(updates == (command.publish && command.result == 2 ?
                     (!strcmp(argv[4], "duplicate") ? 4 : 2) : 0));
    if (registered) {
        unsigned before = updates;
        host.samples = 23;
        host.retired = 123;
        command.result = 0;
        assert(!probe_queue_stats(&ctx));
        assert(command.result == 4 && updates == before && host.retired == 123);
        host.retired = 0;
        command.result = 0;
        assert(!probe_queue_stats(&ctx));
        assert(command.result == 5 && updates == before + 2 && host.samples == 23);
        command.result = 2;
    }
    printf("%lld\n", (long long)command.result);
    return 0;
}
`
	compiler, err := exec.LookPath("cc")
	if err != nil {
		t.Fatal("a native C compiler is required: ", err)
	}
	executable := filepath.Join(t.TempDir(), "iolatency-queue-probe")
	compile := exec.CommandContext(t.Context(), compiler, "-x", "c", "-std=gnu11", "-O2", "-o", executable, "-")
	compile.Stdin = strings.NewReader(program)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile production queue probe: %v\n%s", err, output)
	}
	for _, test := range []struct {
		name, disk, queue, stats, attribute, target, want string
	}{
		{"head without timestamps", "nvme0n1", "legacy", "disabled", "iostats", "nvme0n1", "1\n"},
		{"head with timestamps", "nvme0n1", "legacy", "enabled", "iostats", "nvme0n1", "1\n"},
		{"hidden NVMe path", "nvme0c1n1", "mq", "enabled", "iostats", "nvme0c1n1", "2\n"},
		{"NVMe namespace", "nvme0n1", "mq", "enabled", "iostats", "nvme0n1", "2\n"},
		{"NVMe path without timestamps", "nvme0c1n1", "mq", "disabled", "iostats", "nvme0c1n1", "3\n"},
		{"legacy SCSI disk", "sda", "legacy", "enabled", "iostats", "sda", "2\n"},
		{"legacy SCSI without timestamps", "sda", "legacy", "disabled", "iostats", "sda", "3\n"},
		{"other disk", "sdb", "legacy", "enabled", "iostats", "sda", "0\n"},
		{"other attribute", "sda", "legacy", "enabled", "scheduler", "sda", "0\n"},
		{"attribute prefix", "sda", "legacy", "enabled", "iostats-extra", "sda", "0\n"},
	} {
		for _, mode := range []string{"probe", "publish", "duplicate"} {
			for _, layout := range []string{"accounting", "enable-accounting"} {
				t.Run(test.name+"/"+mode+"/"+layout, func(t *testing.T) {
					output, err := exec.CommandContext(t.Context(), executable, test.disk, test.queue, test.stats, mode, test.attribute, test.target, layout).CombinedOutput()
					if err != nil || string(output) != test.want {
						t.Fatalf("queue probe = %q, %v; want %q", output, err, test.want)
					}
				})
			}
		}
	}
}

type fakeIOLatencyQueueBPF struct {
	bpf.BPF
	result     int64
	request    ioLatencyQueueProbe
	requests   []ioLatencyQueueProbe
	keys       [][]byte
	armed      bool
	readErr    error
	writeErr   error
	deleteErr  error
	publishErr error
}

func (b *fakeIOLatencyQueueBPF) AttachWithOptions(_ []bpf.AttachOption) error { return nil }
func (b *fakeIOLatencyQueueBPF) MapIDByName(name string) uint32 {
	return 1
}

func (b *fakeIOLatencyQueueBPF) WriteMapItems(id uint32, items []bpf.MapItem) error {
	if b.armed && !bytes.Equal(items[0].Key, b.keys[len(b.keys)-1]) {
		return unix.E2BIG
	}
	if err := decodeBPFMapData(items[0].Value, &b.request); err != nil {
		return err
	}
	b.requests = append(b.requests, b.request)
	b.keys = append(b.keys, bytes.Clone(items[0].Key))
	b.armed = b.writeErr == nil
	return b.writeErr
}

func (b *fakeIOLatencyQueueBPF) DeleteMapItems(_ uint32, keys [][]byte) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	if !b.armed || !bytes.Equal(keys[0], b.keys[len(b.keys)-1]) {
		return unix.ENOENT
	}
	b.armed = false
	return nil
}

func (b *fakeIOLatencyQueueBPF) DumpMapByName(_ string) ([]bpf.MapItem, error) {
	if !b.armed {
		return nil, nil
	}
	return []bpf.MapItem{{
		Key: b.keys[len(b.keys)-1], Value: bytesutil.ToBytes(b.request),
	}}, nil
}

func (b *fakeIOLatencyQueueBPF) ReadMap(_ uint32, key []byte) ([]byte, error) {
	if !b.armed || !bytes.Equal(key, b.keys[len(b.keys)-1]) {
		return nil, unix.ENOENT
	}
	if b.request.Publish != 0 && b.publishErr != nil {
		return nil, b.publishErr
	}
	value := b.request
	value.Result = b.result
	value.Initial.Major, value.Initial.Minor = 259, 7
	value.Disk = 0xffff888012340000
	return bytesutil.ToBytes(value), b.readErr
}

func TestIOLatencyQueueProbeDiskIsolation(t *testing.T) {
	root := t.TempDir()
	for _, disk := range []string{"sda", "sdb"} {
		path := filepath.Join(root, "block", disk, "queue")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "iostats"), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	object := &fakeIOLatencyQueueBPF{result: 2}
	session := &ioLatencySession{object: object}
	disks, err := session.discoverDisks(root, true)
	if err != nil || len(disks) != 2 || object.armed || session.diskProbeName != [32]byte{} {
		t.Fatalf("disk discovery: %v, %v; armed=%t", disks, err, object.armed)
	}
	for i, want := range []string{"sda", "sda", "sdb", "sdb"} {
		if got := string(bytes.TrimRight(object.keys[i], "\x00")); got != want {
			t.Fatalf("request %d targeted %q, want %q", i, got, want)
		}
	}
}

// Software blk-crypto probe results.

func (b *fakeIOLatencyCryptoBPF) AttachWithOptions(_ []bpf.AttachOption) error {
	return b.attachErr
}

func (b *fakeIOLatencyCryptoBPF) MapIDByName(_ string) uint32 { return 1 }

func (b *fakeIOLatencyCryptoBPF) ReadMap(_ uint32, _ []byte) ([]byte, error) {
	value := make([]byte, 8)
	netutil.NativeEndian.PutUint64(value, uint64(b.result))
	return value, b.readErr
}

func TestIOLatencyCryptoProbeResult(t *testing.T) {
	for _, test := range []struct {
		name string
		bpf  fakeIOLatencyCryptoBPF
		want error
	}{
		{name: "all modes uninitialized", bpf: fakeIOLatencyCryptoBPF{result: 1}},
		{name: "initialized mode stops tracing", bpf: fakeIOLatencyCryptoBPF{result: 2}, want: types.ErrTracingStopped},
		{name: "kernel data read error", bpf: fakeIOLatencyCryptoBPF{result: -int64(unix.EFAULT)}, want: unix.EFAULT},
		{name: "attach failure", bpf: fakeIOLatencyCryptoBPF{attachErr: unix.EPERM}, want: unix.EPERM},
		{name: "map read failure", bpf: fakeIOLatencyCryptoBPF{readErr: unix.EIO}, want: unix.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := readIOLatencyCryptoProbe(&test.bpf); !errors.Is(err, test.want) {
				t.Fatalf("probe error = %v; want %v", err, test.want)
			}
		})
	}
	if err := readIOLatencyCryptoProbe(&fakeIOLatencyCryptoBPF{}); err == nil {
		t.Fatal("an unexecuted probe was accepted as disabled fallback")
	}
}

// Startup-probe objects and live kernel qualification.

func TestIOLatencyKernelProbeObject(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return test file")
	}
	spec, err := ebpf.LoadCollectionSpec(filepath.Join(
		filepath.Dir(file), "..", "..", "bpf", "iolatency_kernel_probe.o"))
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.RewriteConstants(map[string]any{
		"io_latency_tfms_inited":  uint64(0xffff888000000000),
		"io_latency_crypto_modes": uint32(5),
	}); err != nil {
		t.Fatal(err)
	}
	if program := spec.Programs["probe_crypto_fallback"]; program == nil ||
		program.Type != ebpf.RawTracepoint || program.AttachTo != "sys_enter" {
		t.Fatalf("crypto probe program = %#v", program)
	}
	result := spec.Maps["io_latency_crypto_map"]
	if result == nil || result.Type != ebpf.Array || result.KeySize != 4 ||
		result.ValueSize != 8 || result.MaxEntries != 1 {
		t.Fatalf("crypto result map = %#v", result)
	}
	spec, err = ebpf.LoadCollectionSpec(filepath.Join(
		filepath.Dir(file), "..", "..", "bpf", "iolatency_tracing.o"))
	if err != nil {
		t.Fatal(err)
	}
	if program := spec.Programs["probe_queue_stats"]; program == nil ||
		program.Type != ebpf.Kprobe || program.AttachTo != "queue_attr_show" {
		t.Fatalf("queue probe program = %#v", program)
	}
	result = spec.Maps["io_latency_queue_probe"]
	if result == nil || result.Type != ebpf.Hash || result.KeySize != 32 ||
		result.ValueSize != uint32(binary.Size(ioLatencyQueueProbe{})) || result.MaxEntries != 1 ||
		result.Flags != unix.BPF_F_NO_PREALLOC {
		t.Fatalf("queue result map = %#v", result)
	}
}

// Read real kernel data even on qualification kernels without blk-crypto.
// linux_banner supplies a nonzero byte; address zero exercises the helper's
// actual failed-read path. Neither case creates or injects block IO state.
func TestIOLatencyCryptoProbeKernel(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to read live kernel data")
	}
	if err := bpf.Init(&bpf.Option{}); err != nil {
		t.Fatal(err)
	}
	defer bpf.Shutdown()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not return test file")
	}
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = filepath.Join(filepath.Dir(file), "..", "..", "bpf")
	t.Cleanup(func() { bpf.DefaultObjDir = previous })
	addresses, err := symbol.KsymbolSearchAddresses("linux_banner")
	if err != nil {
		t.Fatal(err)
	}
	if addresses["linux_banner"] == 0 {
		t.Fatal("linux_banner is unavailable")
	}
	for _, test := range []struct {
		name    string
		address uint64
		want    error
	}{
		{name: "initialized byte", address: addresses["linux_banner"], want: types.ErrTracingStopped},
		{name: "failed read", want: unix.EFAULT},
	} {
		t.Run(test.name, func(t *testing.T) {
			object, err := bpf.LoadBPF("iolatency_kernel_probe.o", map[string]any{
				"io_latency_tfms_inited":  test.address,
				"io_latency_crypto_modes": uint32(1),
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := object.Close(); err != nil {
					t.Errorf("close kernel data probe: %v", err)
				}
			})
			if err := readIOLatencyCryptoProbe(object); !errors.Is(err, test.want) {
				t.Fatalf("kernel data probe = %v, want %v", err, test.want)
			}
		})
	}
}

// Real disk configuration and IO qualifications.

// Opt in with an idle test disk. Restore its original setting on every exit;
// the sequence exercises a real sysfs write, notification and disk cleanup.
func runIOLatencyDiskConfigQualification(t *testing.T, session *ioLatencySession) {
	t.Helper()
	disk := os.Getenv("TEST_IOLATENCY_CONFIG_DISK")
	if disk == "" {
		return
	}
	require.Equal(t, filepath.Base(disk), disk, "use a disk name such as sdb")
	path := filepath.Join("/sys/block", disk, "queue/iostats")
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "1", strings.TrimSpace(string(original)))
	t.Cleanup(func() { require.NoError(t, os.WriteFile(path, original, 0o600)) })
	dev, err := os.ReadFile(filepath.Join("/sys/block", disk, "dev"))
	require.NoError(t, err)
	var major, minor uint32
	_, err = fmt.Sscanf(string(dev), "%d:%d", &major, &minor)
	require.NoError(t, err)
	key := ioLatencyHostKey{Major: major, Minor: minor}
	before, _, err := session.captureSnapshot()
	require.NoError(t, err)
	require.Contains(t, before.host, key, "test disk must be enrolled")
	session.previous = before
	for _, enabled := range []bool{false, true} {
		value := []byte("0\n")
		if enabled {
			value = original
		}
		require.NoError(t, os.WriteFile(path, value, 0o600))
		select {
		case <-session.diskEvents:
		case <-time.After(3 * time.Second):
			t.Fatal("queue configuration write produced no disk notification")
		}
		session.disksNeedScan = true
		require.NoError(t, session.refreshDisks(false))
		current, _, err := session.captureSnapshot()
		require.NoError(t, err)
		_, visible := current.host[key]
		require.Equal(t, enabled, visible, "disk configuration enabled=%t", enabled)
		for other := range before.host {
			if other.Major != major || other.Minor != minor {
				require.Contains(t, current.host, other, "another disk lost admission")
			}
		}
		require.NotContains(t, session.previous.host, key, "transition baseline must be discarded")
		require.NoError(t, session.checkHealth())
	}
	t.Logf("disk %s disabled and re-enabled in the same BPF object", disk)
}

func runIOLatencyReadQualification(t *testing.T, object bpf.BPF) {
	t.Helper()
	device := os.Getenv("TEST_IOLATENCY_IO_DEVICE")
	if device == "" {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 210*time.Second)
	defer cancel()
	device, err := filepath.EvalSymlinks(device)
	require.NoError(t, err)
	var stat unix.Stat_t
	require.NoError(t, unix.Stat(device, &stat))
	require.Equal(t, uint32(unix.S_IFBLK), stat.Mode&unix.S_IFMT,
		"TEST_IOLATENCY_IO_DEVICE must be an existing block device")
	major, minor := unix.Major(stat.Rdev), unix.Minor(stat.Rdev)
	sysPath, err := filepath.EvalSymlinks(fmt.Sprintf("/sys/dev/block/%d:%d", major, minor))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(sysPath, "partition"))
	require.ErrorIs(t, err, os.ErrNotExist, "qualification requires a whole disk, not a partition")

	drain := func() {
		t.Helper()
		drainCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		idle := 0
		for {
			require.NoError(t, drainCtx.Err(), "waiting for %s IO to drain", device)
			data, err := os.ReadFile(filepath.Join(sysPath, "inflight"))
			require.NoError(t, err)
			var reads, writes uint64
			n, err := fmt.Sscanf(string(data), "%d %d", &reads, &writes)
			require.NoError(t, err)
			require.Equal(t, 2, n)
			if reads == 0 && writes == 0 {
				idle++
			} else {
				idle = 0
			}
			if idle == 2 {
				return
			}
			select {
			case <-drainCtx.Done():
				t.Fatalf("waiting for %s IO to drain: %v", device, drainCtx.Err())
			case <-ticker.C:
			}
		}
	}
	session := ioLatencySession{object: object}
	snapshot := func(round int) ioLatencyCounters {
		t.Helper()
		require.NoError(t, ctx.Err())
		require.NoError(t, session.checkHealth())
		samples, _, err := session.captureHostLatency(map[[2]uint32]string{
			{major, minor}: filepath.Base(sysPath),
		})
		require.NoError(t, err)
		items, err := object.DumpMapByName("bio_latency_map")
		require.NoError(t, err)
		var addresses []uint64
		inactive := 0
		for _, item := range items {
			var address uint64
			var state ioLatencyBioState
			require.NoError(t, decodeBPFMapData(item.Key, &address))
			require.NoError(t, decodeBPFMapData(item.Value, &state))
			if state.Major == major && state.Minor == minor {
				if state.QueueNS == 0 {
					inactive++
				} else {
					addresses = append(addresses, address)
				}
			}
		}
		slices.Sort(addresses)
		t.Logf("round=%d device=%s (%d:%d) bio_active=%d bio_inactive=%d active_addresses=%#x total_map_entries=%d",
			round, device, major, minor, len(addresses), inactive, addresses, len(items))
		require.NoError(t, session.checkHealth())
		require.NoError(t, ctx.Err())
		if sample := samples[ioLatencyHostKey{Major: major, Minor: minor}]; sample != nil {
			return sample.counters // REQ_OP_READ is zero.
		}
		return ioLatencyCounters{}
	}

	t.Logf("read-only qualification: device=%s (%d:%d), latency_bounds=%v, size_bounds=%v",
		device, major, minor, ioLatencyBucketLabels, ioSizeBucketLabels)
	t.Log("fio request sizes/counts do not establish bio-chain coverage; observe bi_next separately")
	drain()
	previous := snapshot(0)
	for round := 1; round <= 3; round++ {
		command := exec.CommandContext(ctx, "timeout", "--kill-after=5s", "30s", "fio",
			"--name=global", "--readonly", "--filename="+device, "--allow_file_create=0",
			"--ioengine=libaio", "--direct=1", "--size=128m", "--time_based=1",
			"--runtime=5", "--group_reporting=1", "--exitall_on_error=1",
			"--name=direct-read", "--rw=read", "--bs=4k", "--iodepth=1",
			"--name=large-read", "--stonewall", "--rw=read", "--bs=4m", "--iodepth=16",
			"--name=merge-read", "--stonewall", "--rw=read", "--bs=512", "--iodepth=256",
			"--iodepth_batch_submit=256", "--iodepth_batch_complete_min=1",
			"--iodepth_batch_complete_max=256", "--output-format=json")
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error { return unix.Kill(-command.Process.Pid, unix.SIGKILL) }
		command.WaitDelay = 5 * time.Second
		output, err := command.CombinedOutput()
		t.Logf("round=%d fio: %s", round, output)
		require.NoError(t, err, "read-only fio round %d", round)
		drain()
		current := snapshot(round)
		delta, monotonic := ioLatencyCountersDelta(&previous, &current)
		require.True(t, monotonic, "production counters decreased in round %d", round)
		for stage, definition := range ioLatencyStageMetrics {
			var count uint64
			for _, value := range delta.Buckets[stage] {
				count += value
			}
			t.Logf("round=%d device=%d:%d read stage=%s samples=%d buckets=%v",
				round, major, minor, definition.name, count, delta.Buckets[stage])
			if count == 0 {
				t.Errorf("round %d: %s produced no read samples", round, definition.name)
			}
		}
		t.Logf("round=%d device=%d:%d read queued_size=%v issued_size=%v",
			round, major, minor, delta.QueuedSize, delta.IssuedSize)
		previous = current
	}
}
