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

// Shared IO counter and kernel-layout contracts are checked with reusable fixtures.

package collector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/pkg/metric"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/pelletier/go-toml"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/encoding/protodelim"
)

func TestIOControlAttachRetries(t *testing.T) {
	for _, test := range []struct {
		name     string
		failure  error
		failures int
		attempts int
		want     error
	}{
		{name: "available", attempts: 1},
		{name: "missing", failure: unix.ENOENT, failures: 1, attempts: 1, want: types.ErrNotSupported},
		{name: "unsupported", failure: unix.EOPNOTSUPP, failures: 1, attempts: 1, want: types.ErrNotSupported},
		{name: "permission", failure: unix.EPERM, failures: 1, attempts: 1, want: unix.EPERM},
		{name: "transient", failure: unix.EIO, failures: 2, attempts: 3},
		{name: "exhausted", failure: unix.EIO, failures: 4, attempts: 3, want: unix.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			err := retryIOControlAttach(t.Context(), time.Millisecond, func() error {
				attempts++
				if attempts <= test.failures {
					return test.failure
				}
				return nil
			})
			require.ErrorIs(t, err, test.want)
			require.Equal(t, test.attempts, attempts)
			if test.want != nil {
				require.ErrorIs(t, err, test.failure)
			}
		})
	}
	t.Run("cancel retry wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		attempts := 0
		err := retryIOControlAttach(ctx, time.Hour, func() error {
			attempts++
			cancel()
			return unix.EIO
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, attempts)
	})
}

type ioControlDeviceBPF struct {
	bpf.BPF
	mapID uint32
	value []byte
	err   error
}

func (b *ioControlDeviceBPF) MapIDByName(name string) uint32 {
	if name != ioControlStatusMap {
		return 0
	}
	return b.mapID
}

func (b *ioControlDeviceBPF) ReadMap(uint32, []byte) ([]byte, error) {
	return b.value, b.err
}

func ioControlTestStatusBytes(failure, devicesChanged uint64) []byte {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint64(data[:8], failure)
	binary.LittleEndian.PutUint64(data[8:], devicesChanged)
	return data
}

func ioControlTestStatus(reason uint32, errno int32) ioControlStatus {
	return ioControlStatus{Failure: uint64(reason)<<32 | uint64(uint32(errno))}
}

func TestIOControlStatus(t *testing.T) {
	t.Run("independent fields", func(t *testing.T) {
		object := &ioControlDeviceBPF{
			mapID: 1,
			value: []byte{
				0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01,
				0x18, 0x17, 0x16, 0x15, 0x14, 0x13, 0x12, 0x11,
			},
		}
		got, err := readIOControlStatus(object)
		require.NoError(t, err)
		require.Equal(t, ioControlStatus{
			Failure: 0x0102030405060708, DevicesChanged: 0x1112131415161718,
		}, got)
	})

	t.Run("exact ABI", func(t *testing.T) {
		for _, size := range []int{0, 1, 8, 15, 17, 24} {
			t.Run(fmt.Sprint(size), func(t *testing.T) {
				got, err := readIOControlStatus(&ioControlDeviceBPF{
					mapID: 1, value: make([]byte, size),
				})
				require.ErrorIs(t, err, types.ErrTracingStopped)
				require.Zero(t, got)
			})
		}
	})
}

func TestIOControlDeviceChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		object ioControlDeviceBPF
		want   error
	}{
		{name: "unchanged", object: ioControlDeviceBPF{mapID: 1, value: ioControlTestStatusBytes(0, 0)}},
		{name: "changed", object: ioControlDeviceBPF{mapID: 1, value: ioControlTestStatusBytes(0, 1)}, want: errIOControlDevicesChanged},
		{name: "failure only", object: ioControlDeviceBPF{mapID: 1, value: ioControlTestStatusBytes(4, 0)}},
		{name: "failure cannot hide device change", object: ioControlDeviceBPF{mapID: 1, value: ioControlTestStatusBytes(4, 1)}, want: errIOControlDevicesChanged},
		{name: "missing map", want: types.ErrTracingStopped},
		{name: "invalid ABI", object: ioControlDeviceBPF{mapID: 1, value: []byte{1}}, want: types.ErrTracingStopped},
		{name: "read failed", object: ioControlDeviceBPF{mapID: 1, err: unix.EIO}, want: unix.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkIOControlDeviceChanges(&test.object)
			require.ErrorIs(t, err, test.want)
			if errors.Is(test.want, errIOControlDevicesChanged) {
				require.NotErrorIs(t, err, types.ErrTracingStopped)
			}
		})
	}
}

func ioControlBPFSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "bpf", name))
	require.NoError(t, err)
	return string(data)
}

func ioControlTestBytes(value any) []byte {
	data, err := binary.Append(nil, binary.LittleEndian, value)
	if err != nil {
		panic(err)
	}
	return data
}

func ioControlTestMapItem(key, value any) bpf.MapItem {
	return bpf.MapItem{Key: ioControlTestBytes(key), Value: ioControlTestBytes(value)}
}

type ioControlTestMapContract struct {
	name       string
	typeID     ebpf.MapType
	keySize    uint32
	valueSize  uint32
	maxEntries uint32
	flags      uint32
}

func requireIOControlTestMap(t *testing.T, spec *ebpf.CollectionSpec, want ioControlTestMapContract) {
	t.Helper()
	actual, ok := spec.Maps[want.name]
	require.True(t, ok, "map %s", want.name)
	require.Equal(t, want.typeID, actual.Type, "map %s type", want.name)
	require.Equal(t, want.keySize, actual.KeySize, "map %s key size", want.name)
	require.Equal(t, want.valueSize, actual.ValueSize, "map %s value size", want.name)
	require.Equal(t, want.maxEntries, actual.MaxEntries, "map %s capacity", want.name)
	require.Equal(t, want.flags, actual.Flags, "map %s flags", want.name)
}

func compileIOControlTestC(t *testing.T, compiler, name, source string, flags ...string) string {
	t.Helper()
	path, err := exec.LookPath(compiler)
	require.NoError(t, err, "a native %s compiler is required", compiler)
	executable := filepath.Join(t.TempDir(), name)
	args := append([]string{"-x", "c", "-O2"}, flags...)
	args = append(args, "-o", executable, "-")
	command := exec.CommandContext(t.Context(), path, args...)
	command.Stdin = strings.NewReader(source)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "compile %s: %s", name, output)
	return executable
}

func ioControlTestSourceSection(t *testing.T, source, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	require.GreaterOrEqual(t, start, 0, "missing source marker %q", startMarker)
	end := strings.Index(source[start+len(startMarker):], endMarker)
	require.GreaterOrEqual(t, end, 0, "missing source marker %q", endMarker)
	return source[start : start+len(startMarker)+end]
}

func ioControlTestDiskNames() map[[2]uint32]string {
	return map[[2]uint32]string{
		{8, 0}: "sda", {8, 1}: "sda1", {8, 16}: "sdb",
		{9, 0}: "md0", {259, 0}: "nvme0n1", {259, 7}: "nvme0n7",
		{4095, 1}: "sda", {4094, 2}: "nvme0n1",
	}
}

func ioControlTestDiskstats(t *testing.T) {
	t.Helper()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	root := t.TempDir()
	path := filepath.Join(root, "proc")
	require.NoError(t, os.MkdirAll(path, 0o755))
	var data string
	for key, name := range ioControlTestDiskNames() {
		data += fmt.Sprintf("%d %d %s 0 0 0 0 0 0 0 0 0 0 0\n", key[0], key[1], name)
	}
	require.NoError(t, os.WriteFile(filepath.Join(path, "diskstats"), []byte(data), 0o600))
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
}

func TestIOControlWaitCounterDecoding(t *testing.T) {
	t.Run("packed field boundaries", func(t *testing.T) {
		for _, packed := range []uint64{0, 1, uint64(1) << 37, uint64(1) << 38, ^uint64(0)} {
			data := make([]byte, ioControlWaitCounterSize)
			binary.LittleEndian.PutUint64(data, packed)
			got, err := decodeIOControlWaitCounters(data, 1)
			require.NoError(t, err)
			require.Equal(t, []ioControlWaitCounters{{
				IOCount: packed >> 38, Wait10US: packed & ((1 << 38) - 1),
			}}, got)
		}
	})

	t.Run("golden bytes and CPU lanes", func(t *testing.T) {
		data := []byte{
			0x03, 0x00, 0x00, 0x00, 0x80, 0x00, 0x00, 0x00,
			0x07, 0x00, 0x00, 0x00, 0x80, 0x01, 0x00, 0x00,
		}
		want := []ioControlWaitCounters{
			{IOCount: 2, Wait10US: 3},
			{IOCount: 6, Wait10US: 7},
		}
		for possibleCPUs := 1; possibleCPUs <= 2; possibleCPUs++ {
			got, err := decodeIOControlWaitCounters(data[:possibleCPUs*ioControlWaitCounterSize], possibleCPUs)
			require.NoError(t, err)
			require.Equal(t, want[:possibleCPUs], got)
		}
	})

	t.Run("exact value size", func(t *testing.T) {
		for _, possibleCPUs := range []int{1, 2} {
			for _, size := range []int{0, possibleCPUs*ioControlWaitCounterSize - 1, possibleCPUs*ioControlWaitCounterSize + 1} {
				_, err := decodeIOControlWaitCounters(make([]byte, size), possibleCPUs)
				require.ErrorContains(t, err, "data size", "value size %d, CPUs %d", size, possibleCPUs)
			}
		}
	})

	t.Run("valid CPU count", func(t *testing.T) {
		for _, possibleCPUs := range []int{0, -1} {
			_, err := decodeIOControlWaitCounters(nil, possibleCPUs)
			require.ErrorContains(t, err, "invalid possible CPU count")
		}
		maxInt := int(^uint(0) >> 1)
		_, err := decodeIOControlWaitCounters(nil, maxInt/ioControlWaitCounterSize+1)
		require.ErrorContains(t, err, "too large")
	})
}

type ioControlTestBPF interface {
	bpf.BPF
	dumpBatch(mapID uint32) ([]bpf.MapItem, error)
}

type ioControlWaitSnapshotBPF struct {
	bpf.BPF
	mapName string
	mapID   uint32
	items   []bpf.MapItem
	err     error
}

func (object *ioControlWaitSnapshotBPF) MapIDByName(name string) uint32 {
	if name != object.mapName {
		return 0
	}
	return object.mapID
}

func (object *ioControlWaitSnapshotBPF) dumpBatch(mapID uint32) ([]bpf.MapItem, error) {
	if mapID != object.mapID {
		return nil, fmt.Errorf("unexpected wait counter map ID %d", mapID)
	}
	return object.items, object.err
}

func ioControlWaitTestItem(key ioControlWaitKey, lanes ...ioControlWaitCounters) bpf.MapItem {
	valueData := make([]byte, len(lanes)*ioControlWaitCounterSize)
	for cpu, lane := range lanes {
		packed := lane.IOCount<<ioControlWait10USBits | lane.Wait10US
		binary.LittleEndian.PutUint64(valueData[cpu*ioControlWaitCounterSize:], packed)
	}
	return bpf.MapItem{Key: ioControlTestBytes(key), Value: valueData}
}

func TestIOControlWaitSnapshot(t *testing.T) {
	const mapName = "wait_counter_map"
	key := ioControlWaitKey{CSS: 0x11, CSSSerial: 111, Major: 8, Minor: 1}
	lanes := []ioControlWaitCounters{{IOCount: 2, Wait10US: 3}, {IOCount: 6, Wait10US: 7}}
	item := ioControlWaitTestItem(key, lanes...)

	t.Run("raw identities and CPU lanes", func(t *testing.T) {
		otherKey := key
		otherKey.CSSSerial++
		otherLanes := []ioControlWaitCounters{{IOCount: 4, Wait10US: 8}, {IOCount: 9, Wait10US: 10}}
		writeKey := key
		writeKey.Operation = 1
		object := &ioControlWaitSnapshotBPF{
			mapName: mapName, mapID: 7,
			items: []bpf.MapItem{
				item, ioControlWaitTestItem(otherKey, otherLanes...), ioControlWaitTestItem(writeKey, lanes...),
			},
		}
		got, err := captureIOControlWaitSnapshot(object, &ioControlHashReader{lookupBatch: object.dumpBatch}, mapName, 2)
		require.NoError(t, err)
		require.Equal(t, ioControlWaitSnapshot{key: lanes, otherKey: otherLanes, writeKey: lanes}, got)
	})

	t.Run("empty map", func(t *testing.T) {
		object := &ioControlWaitSnapshotBPF{mapName: mapName, mapID: 7}
		got, err := captureIOControlWaitSnapshot(object, &ioControlHashReader{lookupBatch: object.dumpBatch}, mapName, 2)
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("exact key ABI", func(t *testing.T) {
		for _, size := range []int{0, 8, 16, 24, 31, 33, 40} {
			t.Run(fmt.Sprint(size), func(t *testing.T) {
				keyData := make([]byte, size)
				copy(keyData, item.Key)
				object := &ioControlWaitSnapshotBPF{
					mapName: mapName, mapID: 7,
					items: []bpf.MapItem{{Key: keyData, Value: item.Value}},
				}
				got, err := captureIOControlWaitSnapshot(object, &ioControlHashReader{lookupBatch: object.dumpBatch}, mapName, 2)
				require.ErrorIs(t, err, types.ErrTracingStopped)
				require.Nil(t, got)
			})
		}
	})

	for _, test := range []struct {
		name         string
		key          ioControlWaitKey
		value        []byte
		possibleCPUs int
	}{
		{name: "missing CSS", key: ioControlWaitKey{CSSSerial: 111}, value: item.Value, possibleCPUs: 2},
		{name: "missing CSS serial", key: ioControlWaitKey{CSS: 0x11}, value: item.Value, possibleCPUs: 2},
		{name: "reserved field", key: ioControlWaitKey{CSS: 0x11, CSSSerial: 111, Reserved: 1}, value: item.Value, possibleCPUs: 2},
		{name: "unsupported operation", key: ioControlWaitKey{CSS: 0x11, CSSSerial: 111, Operation: 2}, value: item.Value, possibleCPUs: 2},
		{name: "invalid operation", key: ioControlWaitKey{CSS: 0x11, CSSSerial: 111, Operation: ^uint32(0)}, value: item.Value, possibleCPUs: 2},
		{name: "invalid value ABI", key: key, value: []byte{1}, possibleCPUs: 2},
		{name: "missing CPU lane", key: key, value: item.Value[:8], possibleCPUs: 2},
		{name: "extra CPU lane", key: key, value: append(bytes.Clone(item.Value), make([]byte, 8)...), possibleCPUs: 2},
		{name: "invalid CPU count", key: key, value: item.Value},
	} {
		t.Run(test.name, func(t *testing.T) {
			badItem := ioControlWaitTestItem(test.key)
			badItem.Value = test.value
			object := &ioControlWaitSnapshotBPF{
				mapName: mapName, mapID: 7, items: []bpf.MapItem{badItem},
			}
			got, err := captureIOControlWaitSnapshot(object, &ioControlHashReader{lookupBatch: object.dumpBatch}, mapName, test.possibleCPUs)
			require.ErrorIs(t, err, types.ErrTracingStopped)
			require.Nil(t, got)
		})
	}

	for _, test := range []struct {
		name  string
		mapID uint32
		items []bpf.MapItem
		err   error
		want  error
	}{
		{name: "missing map", want: types.ErrTracingStopped},
		{name: "read failed", mapID: 7, err: unix.EIO, want: unix.EIO},
		{name: "read incomplete", mapID: 7, err: unix.EAGAIN, want: unix.EAGAIN},
		{
			name: "duplicate raw key", mapID: 7,
			items: []bpf.MapItem{item, ioControlWaitTestItem(key,
				ioControlWaitCounters{IOCount: 3, Wait10US: 4}, ioControlWaitCounters{})},
			want: errIOControlSnapshotUnstable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := &ioControlWaitSnapshotBPF{
				mapName: mapName, mapID: test.mapID, items: test.items, err: test.err,
			}
			got, err := captureIOControlWaitSnapshot(object, &ioControlHashReader{lookupBatch: object.dumpBatch}, mapName, 2)
			require.ErrorIs(t, err, test.want)
			if !errors.Is(test.want, types.ErrTracingStopped) {
				require.NotErrorIs(t, err, types.ErrTracingStopped)
			}
			require.Nil(t, got)
		})
	}
}

// Packed fields wrap independently; per-CPU validation precedes public sums.
func TestIOControlWaitCounterDeltas(t *testing.T) {
	for _, test := range []struct {
		name     string
		previous []ioControlWaitCounters
		current  []ioControlWaitCounters
		want     ioControlWaitCounters
		err      string
	}{
		{
			name:     "count wraps",
			previous: []ioControlWaitCounters{{IOCount: ioControlWaitCountMask - 1, Wait10US: 100}},
			current:  []ioControlWaitCounters{{IOCount: 1, Wait10US: 106}},
			want:     ioControlWaitCounters{IOCount: 3, Wait10US: 6},
		},
		{
			name:     "wait wraps",
			previous: []ioControlWaitCounters{{IOCount: 5, Wait10US: ioControlWait10USMask - 1}},
			current:  []ioControlWaitCounters{{IOCount: 7, Wait10US: 1}},
			want:     ioControlWaitCounters{IOCount: 2, Wait10US: 3},
		},
		{
			name:     "sum CPUs after subtraction",
			previous: []ioControlWaitCounters{{IOCount: 2, Wait10US: 10}, {}},
			current:  []ioControlWaitCounters{{IOCount: 3, Wait10US: 15}, {IOCount: 2}},
			want:     ioControlWaitCounters{IOCount: 3, Wait10US: 5},
		},
		{name: "empty lanes", err: "empty per-CPU counters"},
		{
			name:     "CPU count changed",
			previous: []ioControlWaitCounters{{}, {}},
			current:  []ioControlWaitCounters{{}}, err: "counter count changed",
		},
		{
			name:    "wait without completed IO",
			current: []ioControlWaitCounters{{Wait10US: 1}}, err: "with no delayed IO",
		},
		{
			name:    "another CPU cannot hide an invalid lane",
			current: []ioControlWaitCounters{{Wait10US: 5}, {IOCount: 1}}, err: "CPU 0 delta",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := deltaIOControlWaitCounters(test.previous, test.current)
			if test.err != "" {
				require.ErrorContains(t, err, test.err)
				require.Zero(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}

	t.Run("full map and maximum CPU count fit uint64", func(t *testing.T) {
		lanes := make([]ioControlWaitCounters, 8192)
		for cpu := range lanes {
			lanes[cpu] = ioControlWaitCounters{IOCount: ioControlWaitCountMask, Wait10US: ioControlWait10USMask}
		}
		row, err := deltaIOControlWaitCounters(nil, lanes)
		require.NoError(t, err)
		intervals := newIOControlWaitIntervals()
		for range 8192 {
			intervals.add(nil, 0, "8:0", "read", row)
		}
		key := ioControlDeviceOperation{device: "8:0", operation: "read"}
		want := ioControlWaitCounters{
			IOCount:  (1 << 26) * ioControlWaitCountMask,
			Wait10US: (1 << 26) * ioControlWait10USMask,
		}
		require.Equal(t, want, intervals.host[key])
		require.Equal(t, want, intervals.other[key])
	})
}

func ioControlWaitTestMetricDefinition() ioControlWaitMetrics {
	return ioControlWaitMetrics{
		countName: "delayed_io_count", countHelp: "Delayed IO count.",
		averageName: "average_wait_ms", averageHelp: "Average wait in milliseconds.",
	}
}

func TestIOControlWaitMetricsRawBaselines(t *testing.T) {
	key := ioControlWaitKey{CSS: 0x11, CSSSerial: 111, Major: 8, Minor: 1}
	replacementKey := key
	replacementKey.CSSSerial++
	baseline := ioControlWaitSnapshot{
		key: {{IOCount: 10, Wait10US: 1_000}, {IOCount: 20, Wait10US: 2_000}},
	}
	definition := ioControlWaitTestMetricDefinition()
	for _, test := range []struct {
		name          string
		previous      ioControlWaitSnapshot
		current       ioControlWaitSnapshot
		wantCount     float64
		wantAverageMS float64
		empty         bool
	}{
		{name: "empty snapshots", empty: true},
		{
			name: "first snapshot", current: ioControlWaitSnapshot{
				key: {{IOCount: 2, Wait10US: 400}, {IOCount: 3, Wait10US: 900}},
			}, wantCount: 5, wantAverageMS: 2.6,
		},
		{
			name: "same raw lifetime", previous: baseline, current: ioControlWaitSnapshot{
				key: {{IOCount: 11, Wait10US: 1_200}, {IOCount: 23, Wait10US: 2_900}},
			}, wantCount: 4, wantAverageMS: 2.75,
		},
		{
			name: "replacement serial starts its own baseline", previous: baseline,
			current: ioControlWaitSnapshot{
				replacementKey: {{IOCount: 1, Wait10US: 200}, {}},
			}, wantCount: 1, wantAverageMS: 2,
		},
		{name: "no new IO", previous: baseline, current: baseline},
		{name: "retired raw identity", previous: baseline, empty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildIOControlWaitMetrics(test.previous, test.current, nil, ioControlTestDiskNames(), definition)
			require.NoError(t, err)
			if test.empty {
				require.Empty(t, got)
				return
			}
			var want []*metric.Data
			for _, scope := range []string{ioControlHostScope, ioControlOtherScope} {
				labels := map[string]string{"disk": "sda1", "operation": "read", "scope": scope}
				want = append(want,
					metric.NewGaugeData(definition.countName, test.wantCount, definition.countHelp, labels),
					metric.NewGaugeData(definition.averageName, test.wantAverageMS, definition.averageHelp, labels),
				)
			}
			require.ElementsMatch(t, want, got)
		})
	}
}

func TestIOControlWaitMetricsRejectInvalidCPUDeltas(t *testing.T) {
	key := ioControlWaitKey{CSS: 0x11, CSSSerial: 111, Major: 8}
	for _, test := range []struct {
		name     string
		previous []ioControlWaitCounters
		current  []ioControlWaitCounters
		want     string
	}{
		{name: "empty lanes", want: "empty per-CPU counters"},
		{
			name: "CPU count changed", previous: []ioControlWaitCounters{{}, {}},
			current: []ioControlWaitCounters{{}}, want: "counter count changed",
		},
		{
			name: "wait without delayed IO", current: []ioControlWaitCounters{{Wait10US: 1}},
			want: "with no delayed IO",
		},
		{
			name:    "another CPU cannot hide an invalid lane",
			current: []ioControlWaitCounters{{Wait10US: 5}, {IOCount: 1}}, want: "CPU 0 delta",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildIOControlWaitMetrics(
				ioControlWaitSnapshot{key: test.previous}, ioControlWaitSnapshot{key: test.current},
				nil, ioControlTestDiskNames(), ioControlWaitTestMetricDefinition(),
			)
			require.ErrorContains(t, err, test.want)
			require.Nil(t, got)
		})
	}
}

func TestIOControlWaitMetricsMergesLabelsAfterCPUDeltas(t *testing.T) {
	startedAt := time.Unix(100, 0)
	containerA := newIOControlAttributionTestContainer("container-a", 0x11, startedAt)
	containerB := newIOControlAttributionTestContainer("container-b", 0x22, startedAt.Add(time.Second))
	containers := pod.BuildCssContainers(map[string]*pod.Container{
		containerA.ID: containerA, containerB.ID: containerB,
	}, subsystem.SubsystemBlkIO)
	keyA := ioControlWaitKey{CSS: 0x11, CSSSerial: 111, Major: 8}
	keyB := ioControlWaitKey{CSS: 0x22, CSSSerial: 222, Major: 8}
	newSerialA := ioControlWaitKey{CSS: 0x11, CSSSerial: 444, Major: 8}
	otherKey := ioControlWaitKey{CSS: 0x33, CSSSerial: 333, Major: 8}
	previous := ioControlWaitSnapshot{
		keyA: {
			{IOCount: ioControlWaitCountMask - 1, Wait10US: ioControlWait10USMask - 100},
			{IOCount: 2, Wait10US: 500},
		},
		keyB:     {{IOCount: 20, Wait10US: 2_000}, {IOCount: 15, Wait10US: 1_500}},
		otherKey: {{IOCount: 70, Wait10US: 7_000}, {}},
	}
	current := ioControlWaitSnapshot{
		keyA:       {{IOCount: 1, Wait10US: 99}, {IOCount: 4, Wait10US: 1_100}},
		keyB:       {{IOCount: 23, Wait10US: 2_900}, {IOCount: 15, Wait10US: 1_500}},
		newSerialA: {{IOCount: 11, Wait10US: 5_500}, {}},
		otherKey:   {{IOCount: 77, Wait10US: 9_800}, {}},
	}
	definition := ioControlWaitTestMetricDefinition()
	got, err := buildIOControlWaitMetrics(previous, current, containers, ioControlTestDiskNames(), definition)
	require.NoError(t, err)
	containerLabels := map[string]string{"disk": "sda", "operation": "read"}
	want := []*metric.Data{
		metric.NewContainerGaugeData(containerA, definition.countName, 19, definition.countHelp, containerLabels),
		metric.NewContainerGaugeData(containerA, definition.averageName, 72.0/19, definition.averageHelp, containerLabels),
	}
	for _, scope := range []struct {
		name    string
		count   float64
		average float64
	}{
		{name: ioControlHostScope, count: 26, average: 100.0 / 26},
		{name: ioControlOtherScope, count: 7, average: 4},
	} {
		labels := map[string]string{"disk": "sda", "operation": "read", "scope": scope.name}
		want = append(want,
			metric.NewGaugeData(definition.countName, scope.count, definition.countHelp, labels),
			metric.NewGaugeData(definition.averageName, scope.average, definition.averageHelp, labels),
		)
	}
	require.ElementsMatch(t, want, got)

	counts, averages := make(map[string]float64), make(map[string]float64)
	for _, data := range got {
		scope := data.Labels()["scope"]
		switch data.Name() {
		case definition.countName, "container_" + definition.countName:
			counts[scope] = data.Value
		case definition.averageName, "container_" + definition.averageName:
			averages[scope] = data.Value
		}
	}
	require.Equal(t, counts[ioControlHostScope], counts[ioControlOtherScope]+counts[""],
		"Host delayed IO count must equal Container plus Other")
	require.InDelta(t, counts[ioControlHostScope]*averages[ioControlHostScope],
		counts[ioControlOtherScope]*averages[ioControlOtherScope]+counts[""]*averages[""], 1e-12,
		"count-weighted waits must preserve the raw accounting equation")

	again, err := buildIOControlWaitMetrics(previous, current, containers, ioControlTestDiskNames(), definition)
	require.NoError(t, err)
	require.ElementsMatch(t, got, again, "metric construction must leave the raw baselines unchanged")
}

func TestIOControlSingleLookupRequiresLegacyHashAllocator(t *testing.T) {
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
	otherHash := ioControlHashBTFFixture(btf.Member{Name: "count", Type: word})
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
			name:  "two ordinary HASH candidates precede socket HASH",
			types: []btf.Type{legacyHash, otherHash, socket},
		},
		{
			name:  "socket HASH precedes two ordinary HASH candidates",
			types: []btf.Type{socket, otherHash, legacyHash},
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
			err = checkIOControlSingleLookup(spec)
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

func TestIOControlMapAllocation(t *testing.T) {
	for _, allocated := range []bool{false, true} {
		t.Run(fmt.Sprintf("bpf_mem_alloc=%t", allocated), func(t *testing.T) {
			hash := ioControlHashBTFFixture()
			if allocated {
				hash = ioControlHashBTFFixture(btf.Member{
					Name: "pcpu_ma", Type: &btf.Struct{Name: "bpf_mem_alloc", Size: 8},
				})
			}
			builder, err := btf.NewBuilder([]btf.Type{hash})
			require.NoError(t, err)
			raw, err := builder.Marshal(nil, nil)
			require.NoError(t, err)
			kernel, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
			require.NoError(t, err)
			spec := &ebpf.CollectionSpec{Maps: map[string]*ebpf.MapSpec{
				"wait":   {Type: ebpf.PerCPUHash, Flags: unix.BPF_F_NO_PREALLOC | unix.BPF_F_RDONLY},
				"sparse": {Type: ebpf.Hash, Flags: unix.BPF_F_NO_PREALLOC},
			}}
			require.NoError(t, prepareIOControlMaps(spec, kernel))
			want := uint32(unix.BPF_F_NO_PREALLOC | unix.BPF_F_RDONLY)
			if allocated {
				want = unix.BPF_F_RDONLY
			}
			require.Equal(t, want, spec.Maps["wait"].Flags)
			require.EqualValues(t, unix.BPF_F_NO_PREALLOC, spec.Maps["sparse"].Flags)
		})
	}
}

type ioControlHashReadBPF struct {
	bpf.BPF
	err error
}

func (b *ioControlHashReadBPF) dumpBatch(uint32) ([]bpf.MapItem, error) {
	return nil, b.err
}

func (*ioControlHashReadBPF) DumpMap(uint32) ([]bpf.MapItem, error) {
	return nil, errors.New("unexpected unguarded HASH fallback")
}

func TestIOControlHashReadFailure(t *testing.T) {
	for _, err := range []error{unix.EIO, unix.EPERM, unix.EBADF, unix.ENOMEM} {
		object := &ioControlHashReadBPF{err: err}
		reader := ioControlHashReader{lookupBatch: object.dumpBatch}
		_, got := reader.dump(object, 1)
		require.ErrorIs(t, got, err)
	}
}

func TestIOControlHashBatch(t *testing.T) {
	requireIOControlBPFPermission(t)
	possibleCPUs, err := ebpf.PossibleCPU()
	require.NoError(t, err)
	for _, mapType := range []ebpf.MapType{ebpf.Hash, ebpf.PerCPUHash} {
		t.Run(mapType.String(), func(t *testing.T) {
			for _, entries := range []uint32{0, 2, 257} {
				t.Run(strconv.FormatUint(uint64(entries), 10), func(t *testing.T) {
					maxEntries := max(entries, 2)
					var flags uint32
					lanes := 1
					if mapType == ebpf.PerCPUHash {
						flags = unix.BPF_F_NO_PREALLOC
						lanes = possibleCPUs
					}
					m, err := ebpf.NewMap(&ebpf.MapSpec{
						Type: mapType, KeySize: 40, ValueSize: 8,
						MaxEntries: maxEntries, Flags: flags,
					})
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, m.Close()) })
					requireIOControlMapBatch(t, m)

					want := make(map[string][]byte, entries)
					for row := uint32(0); row < entries; row++ {
						key := make([]byte, 40)
						binary.LittleEndian.PutUint32(key, row)
						for offset := 4; offset < len(key); offset++ {
							key[offset] = byte(offset)
						}
						values := make([]uint64, lanes)
						packed := make([]byte, lanes*8)
						for cpu := range values {
							values[cpu] = uint64(row+1)<<38 | uint64(cpu+1)
							binary.LittleEndian.PutUint64(packed[cpu*8:], values[cpu])
						}
						var value any = values
						if mapType == ebpf.Hash {
							value = values[0]
						}
						require.NoError(t, m.Put(key, value))
						want[string(key)] = packed
					}
					items, err := dumpIOControlHashBatch(ioControlTestMapID(t, m))
					require.NoError(t, err)
					require.Len(t, items, int(entries))
					got := make(map[string][]byte, len(items))
					for _, item := range items {
						key := string(item.Key)
						require.NotContains(t, got, key, "batch returned a duplicate key")
						got[key] = item.Value
						value, err := m.LookupBytes(item.Key)
						require.NoError(t, err)
						require.Equal(t, item.Value, value, "batch lookup must retain the map row")
					}
					require.Equal(t, want, got)
				})
			}
		})
	}
}

func TestIOControlHashBatchPropagatesErrno(t *testing.T) {
	requireIOControlBPFPermission(t)
	for _, mapType := range []ebpf.MapType{ebpf.Hash, ebpf.PerCPUHash} {
		t.Run(mapType.String(), func(t *testing.T) {
			var flags uint32
			if mapType == ebpf.PerCPUHash {
				flags = unix.BPF_F_NO_PREALLOC
			}
			m, err := ebpf.NewMap(&ebpf.MapSpec{
				Type: mapType, KeySize: 40, ValueSize: 8, MaxEntries: 2, Flags: flags,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, m.Close()) })
			requireIOControlMapBatch(t, m)

			t.Run("permission", func(t *testing.T) {
				writeOnly, err := ebpf.NewMap(&ebpf.MapSpec{
					Type: mapType, KeySize: 40, ValueSize: 8, MaxEntries: 2,
					Flags: flags | unix.BPF_F_WRONLY,
				})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, writeOnly.Close()) })
				items, err := dumpHashMapBatch(writeOnly)
				require.ErrorIs(t, err, unix.EPERM)
				require.Nil(t, items)
			})

			t.Run("bad fd", func(t *testing.T) {
				require.NoError(t, m.Close())
				items, err := dumpHashMapBatch(m)
				require.ErrorIs(t, err, unix.EBADF)
				require.Nil(t, items)
			})
		})
	}
}

func TestIOControlHashBatchConcurrentPerCPUUpdates(t *testing.T) {
	requireIOControlBPFPermission(t)
	m, err := ebpf.NewMap(&ebpf.MapSpec{
		Type: ebpf.PerCPUHash, KeySize: 40, ValueSize: 8, MaxEntries: 2,
		Flags: unix.BPF_F_NO_PREALLOC,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	requireIOControlMapBatch(t, m)
	possibleCPUs, err := ebpf.PossibleCPU()
	require.NoError(t, err)

	keys := [3][]byte{make([]byte, 40), make([]byte, 40), make([]byte, 40)}
	values := [3][]uint64{
		make([]uint64, possibleCPUs), make([]uint64, possibleCPUs), make([]uint64, possibleCPUs),
	}
	packed := [3][]byte{
		make([]byte, possibleCPUs*8), make([]byte, possibleCPUs*8), make([]byte, possibleCPUs*8),
	}
	for row := range keys {
		binary.LittleEndian.PutUint32(keys[row], uint32(row))
		for cpu := range values[row] {
			values[row][cpu] = uint64(row+1)<<38 | uint64(cpu+1)
			binary.LittleEndian.PutUint64(packed[row][cpu*8:], values[row][cpu])
		}
	}
	require.NoError(t, m.Put(keys[0], values[0]))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	started := make(chan struct{})
	done := make(chan struct{})
	var writerErr error
	go func() {
		defer close(done)
		row := 0
		for updates := 0; ; updates++ {
			if ctx.Err() != nil {
				return
			}
			// Keep a row live while recycling nodes across different keys.
			oldRow := row
			row = (row + 1) % len(keys)
			if err := m.Update(keys[row], values[row], ebpf.UpdateNoExist); err != nil {
				writerErr = fmt.Errorf("insert concurrent row: %w", err)
				return
			}
			if err := m.Delete(keys[oldRow]); err != nil {
				writerErr = fmt.Errorf("delete concurrent row: %w", err)
				return
			}
			if updates == 0 {
				close(started)
			}
		}
	}()
	defer func() {
		cancel()
		<-done
		require.NoError(t, writerErr)
	}()
	select {
	case <-started:
	case <-done:
		require.NoError(t, writerErr)
		t.Fatal("concurrent writer stopped before the first update")
	}
	for snapshot := 0; snapshot < 128; {
		require.NoError(t, ctx.Err(), "concurrent batch collection exceeded its deadline")
		items, err := dumpIOControlHashBatch(ioControlTestMapID(t, m))
		if errors.Is(err, ebpf.ErrIterationAborted) {
			// Different buckets may expose successive generations during churn.
			// A traversal over budget is discarded; still require 128 valid reads.
			require.Nil(t, items)
			continue
		}
		require.NoError(t, err)
		for _, item := range items {
			require.Len(t, item.Key, 40)
			row := binary.LittleEndian.Uint32(item.Key)
			require.Less(t, row, uint32(len(keys)))
			require.Equal(t, keys[row], item.Key)
			require.Equal(t, packed[row], item.Value, "key and CPU values must belong to one row")
		}
		snapshot++
	}
}

func ioControlTestMapID(t *testing.T, m *ebpf.Map) uint32 {
	t.Helper()
	info, err := m.Info()
	require.NoError(t, err)
	id, ok := info.ID()
	require.True(t, ok)
	return uint32(id)
}

func requireIOControlBPFPermission(t *testing.T) {
	t.Helper()
	if err := bpf.Init(&bpf.Option{}); err != nil {
		t.Skipf("BPF initialization unavailable: %v", err)
	}
	m, err := ebpf.NewMap(&ebpf.MapSpec{
		Type: ebpf.Hash, KeySize: 4, ValueSize: 4, MaxEntries: 1,
	})
	if errors.Is(err, ebpf.ErrNotSupported) || errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EACCES) {
		t.Skipf("BPF map creation unavailable: %v", err)
	}
	require.NoError(t, err)
	require.NoError(t, m.Close())
}

func requireIOControlMapBatch(t *testing.T, m *ebpf.Map) {
	t.Helper()
	valueSize := int(m.ValueSize())
	if m.Type() == ebpf.PerCPUHash {
		possibleCPUs, err := ebpf.PossibleCPU()
		require.NoError(t, err)
		valueSize = (valueSize + 7) / 8 * 8 * possibleCPUs
	}
	key := make([]byte, m.KeySize())
	value := make([]byte, valueSize)
	var cursor uint32
	var pin runtime.Pinner
	pin.Pin(&key[0])
	pin.Pin(&value[0])
	pin.Pin(&cursor)
	defer pin.Unpin()

	// Probe the kernel independently so invalid production attributes fail tests.
	attr := struct {
		InBatch   uint64
		OutBatch  uint64
		Keys      uint64
		Values    uint64
		Count     uint32
		MapFD     uint32
		ElemFlags uint64
		Flags     uint64
	}{
		OutBatch: uint64(uintptr(unsafe.Pointer(&cursor))),
		Keys:     uint64(uintptr(unsafe.Pointer(&key[0]))),
		Values:   uint64(uintptr(unsafe.Pointer(&value[0]))),
		Count:    1,
		MapFD:    uint32(m.FD()),
	}
	pin.Pin(&attr)
	_, _, errno := unix.Syscall(unix.SYS_BPF, unix.BPF_MAP_LOOKUP_BATCH,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr))
	if errno == unix.EINVAL || errno == unix.ENOSYS || errno == unix.EOPNOTSUPP {
		t.Skipf("kernel does not support hash map batch lookup: %v", errno)
	}
	require.Equal(t, unix.ENOENT, errno, "empty hash map batch qualification")
	require.Zero(t, attr.Count)
}

func ioControlHashBTFFixture(extra ...btf.Member) *btf.Struct {
	word := &btf.Int{Name: "unsigned long", Size: 8}
	hash := &btf.Struct{Name: "bpf_htab", Size: 24, Members: []btf.Member{
		{Name: "elems", Type: word},
		{Name: "extra_elems", Type: word, Offset: 64},
		{Name: "n_buckets", Type: word, Offset: 128},
	}}
	for _, member := range extra {
		member.Offset = btf.Bits(hash.Size * 8)
		hash.Members = append(hash.Members, member)
		hash.Size += 8
	}
	return hash
}

func newIOControlAttributionTestContainer(
	id string,
	css uint64,
	startedAt time.Time,
) *pod.Container {
	return &pod.Container{
		ID:        id,
		Name:      "shared-name",
		Hostname:  "shared-host",
		Type:      pod.ContainerTypeNormal,
		Qos:       pod.ContainerQosLevelMin,
		StartedAt: startedAt,
		CgroupCss: map[string]uint64{
			subsystem.SubsystemBlkIO: css,
		},
		Labels: map[string]any{
			ioControlHostNamespaceKey: "shared-namespace",
		},
	}
}

type ioControlAttributionTestSource struct {
	containers map[string]*pod.Container
	err        error
}

func (source *ioControlAttributionTestSource) read() (
	map[string]*pod.Container,
	error,
) {
	return source.containers, source.err
}

// Start the real daemon with one IO collector; use its native protobuf response
// so metric decoding reuses the project's existing direct dependencies.
func startIOControlKernelDaemon(t *testing.T, parent context.Context, collector string) func() ([]*metric.Data, bool) {
	t.Helper()
	root := filepath.Join("..", "..")
	directory := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	var defaults struct{ BlackList []string }
	data, err := os.ReadFile(filepath.Join(root, "huatuo-bamai.conf"))
	require.NoError(t, err)
	require.NoError(t, toml.Unmarshal(data, &defaults))
	// Preserve normal defaults, excluding sibling IO features and the snapshot
	// tracer whose container subscription requires the disabled kubelet.
	defaults.BlackList = append(defaults.BlackList, "io_health", "blk_throtl", "iolatency", "iocost", "iotracing", "memory_threshold_snapshot")
	blacklist := slices.DeleteFunc(defaults.BlackList, func(name string) bool { return name == collector })
	config := fmt.Sprintf("BlackList = [%s]\n[HTTPServer]\nListenAddress = %q\n[HTTPServer.Auth]\nBearerToken = %q\n",
		`"`+strings.Join(blacklist, `", "`)+`"`, address, "io-kernel-test-token")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "bamai.conf"), []byte(config), 0o600))
	ctx, cancel := context.WithCancel(parent)
	command := exec.CommandContext(ctx, filepath.Join(root, "_output/bin/huatuo-bamai"),
		"--config-dir", directory, "--config", "bamai.conf", "--bpf-dir", filepath.Join(root, "bpf"),
		"--disable-storage", "--disable-kubelet", "--region", "io-kernel-test")
	command.Cancel = func() error { return command.Process.Signal(unix.SIGTERM) }
	command.WaitDelay = 10 * time.Second
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	require.NoError(t, command.Start())
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			t.Logf("%s daemon output:\n%s", collector, output.String())
		case <-time.After(15 * time.Second):
			t.Errorf("%s daemon did not exit", collector)
		}
	})
	client := &http.Client{Timeout: 2 * time.Second}
	scrape := func() ([]*metric.Data, bool) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/metrics", http.NoBody)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer io-kernel-test-token")
		request.Header.Set("Accept", "application/vnd.google.protobuf; proto=io.prometheus.client.MetricFamily; encoding=delimited")
		response, err := client.Do(request)
		if err != nil {
			return nil, false
		}
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Contains(t, response.Header.Get("Content-Type"), "application/vnd.google.protobuf")
		reader := bufio.NewReader(response.Body)
		prefix := "huatuo_bamai_" + collector + "_"
		healthy := false
		var metrics []*metric.Data
		for {
			var family dto.MetricFamily
			if err := protodelim.UnmarshalFrom(reader, &family); errors.Is(err, io.EOF) {
				break
			} else {
				require.NoError(t, err)
			}
			if family.GetName() == "huatuo_bamai_scrape_collector_success" {
				for _, sample := range family.Metric {
					for _, label := range sample.Label {
						if label.GetName() == "collector" && label.GetValue() == collector {
							healthy = sample.GetGauge().GetValue() == 1
						}
					}
				}
			}
			if !strings.HasPrefix(family.GetName(), prefix) {
				continue
			}
			require.False(t, strings.HasPrefix(family.GetName(), prefix+"container_"))
			require.Equal(t, dto.MetricType_GAUGE, family.GetType())
			for _, sample := range family.Metric {
				labels := make(map[string]string)
				for _, label := range sample.Label {
					labels[label.GetName()] = label.GetValue()
				}
				require.Equal(t, "io-kernel-test", labels["region"])
				require.NotEmpty(t, labels["host"])
				metrics = append(metrics, metric.NewGaugeData(strings.TrimPrefix(family.GetName(), prefix),
					sample.GetGauge().GetValue(), family.GetHelp(), labels))
			}
		}
		return metrics, healthy
	}
	require.Eventually(t, func() bool {
		if ctx.Err() != nil {
			return true
		}
		_, healthy := scrape()
		return healthy
	}, 30*time.Second, 100*time.Millisecond, "IO HTTP collector readiness")
	require.NoError(t, ctx.Err())
	return scrape
}

// Body operations must return before the hard timeout so owned cleanup can
// run. Keep cleanup commands on their own bounded, uncanceled contexts.
func ioControlKernelContext(t *testing.T) context.Context {
	t.Helper()
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, unix.SIGTERM)
	t.Cleanup(stop)
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		t.Cleanup(cancel)
	}
	return ctx
}

func waitIOControlKernelState(
	t *testing.T,
	ctx context.Context,
	description string,
	condition func() bool,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		require.NoError(t, ctx.Err(), "waiting for %s", description)
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", description)
}

func runIOControlKernelCommand(
	t *testing.T,
	parent context.Context,
	timeout time.Duration,
	name string,
	arguments ...string,
) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, arguments...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run %s %s: %v: %s", name,
			strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(output))
}

func cleanupIOControlKernelCommand(name string, arguments ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(),
		15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name,
			strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
