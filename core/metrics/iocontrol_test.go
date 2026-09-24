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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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
