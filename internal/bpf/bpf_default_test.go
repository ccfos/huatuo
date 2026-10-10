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

//go:build !didi

package bpf

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	testutils "github.com/ccfos/huatuo/internal/testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if runtime.GOOS != "linux" {
		fmt.Println("skipping tests: requires linux with ebpf support")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestInitAndShutdown(t *testing.T) {
	require.NoError(t, Init(nil))
	Shutdown()
}

// A stable map at capacity must still reach its traversal end.
func TestDefaultBPFDumpFullMap(t *testing.T) {
	requireBPFPermission(t)
	for _, mapType := range []ebpf.MapType{ebpf.Hash, ebpf.Array} {
		t.Run(mapType.String(), func(t *testing.T) {
			m, err := ebpf.NewMap(&ebpf.MapSpec{
				Type: mapType, KeySize: 4, ValueSize: 4, MaxEntries: 2,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = m.Close() })
			for key := uint32(0); key < 2; key++ {
				require.NoError(t, m.Update(key, key+10, ebpf.UpdateAny))
			}
			object := &defaultBPF{mapsByID: map[uint32]loadedMap{1: {handle: m}}}
			items, err := object.DumpMap(1)
			require.NoError(t, err)
			require.Len(t, items, 2, "a stable full map reaches its traversal end")
		})
	}
}

// TestLoad* tests the basic logic of LoadBPFFromBytes and LoadBPF.
func TestLoadBPFFromBytes_InvalidELF(t *testing.T) {
	_, err := LoadBPFFromBytes("invalid", []byte("not-an-elf"), nil)
	require.Error(t, err)
}

// Empty names plus any cleaned form starting with ".." would let LoadBPF
// escape DefaultObjDir once joined.
var rejectedNames = []string{
	"",
	"..",
	"../x.o",
	"../../etc/passwd",
	"x/../../y.o", // cleans to "../y.o"
}

// Path-like CLI inputs (e.g. "./_output/bpf/iotracing.o", absolute paths)
// must pass: they cannot escape DefaultObjDir because Clean keeps them
// at or below the join root.
var acceptedNames = []string{
	"x.o",
	".",
	"./x.o",
	"x/y.o",
	"a..b.o",
	"x/../y.o", // cleans to "y.o"
	"x\\evil.o",
	"/abs/path/x.o",
}

func TestValidateName(t *testing.T) {
	for _, name := range rejectedNames {
		t.Run("reject/"+name, func(t *testing.T) {
			err := validateName(name)
			if !errors.Is(err, errInvalidName) {
				t.Errorf("validateName(%q) = %v, want %v", name, err, errInvalidName)
			}
		})
	}

	for _, name := range acceptedNames {
		t.Run("accept/"+name, func(t *testing.T) {
			if err := validateName(name); err != nil {
				t.Errorf("validateName(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestLoadBPFFromBytes_InvalidName(t *testing.T) {
	for _, name := range rejectedNames {
		t.Run(name, func(t *testing.T) {
			_, err := LoadBPFFromBytes(name, []byte("x"), nil)
			if !errors.Is(err, errInvalidName) {
				t.Errorf("LoadBPFFromBytes(%q) = %v, want %v", name, err, errInvalidName)
			}
		})
	}
}

func TestLoadBPF_InvalidName(t *testing.T) {
	for _, name := range rejectedNames {
		t.Run(name, func(t *testing.T) {
			_, err := LoadBPF(name, nil)
			if !errors.Is(err, errInvalidName) {
				t.Errorf("LoadBPF(%q) = %v, want %v", name, err, errInvalidName)
			}
		})
	}
}

func TestLoadBPF_FileNotFound(t *testing.T) {
	old := DefaultObjDir
	DefaultObjDir = t.TempDir()
	t.Cleanup(func() { DefaultObjDir = old })

	_, err := LoadBPF("definitely_not_exists.o", nil)
	require.Error(t, err)
}

func TestLoadBPF_DefaultObjDir_Empty(t *testing.T) {
	old := DefaultObjDir
	DefaultObjDir = ""
	t.Cleanup(func() { DefaultObjDir = old })

	_, err := LoadBPF("definitely_not_exists.o", nil)
	require.Error(t, err)
}

func TestLoadBPF_DefaultObjDir_Relative(t *testing.T) {
	old := DefaultObjDir
	DefaultObjDir = "./definitely_not_exists_dir"
	t.Cleanup(func() { DefaultObjDir = old })

	_, err := LoadBPF("definitely_not_exists.o", nil)
	require.Error(t, err)
}

func TestLoadBPF_DefaultObjDir_Unreadable(t *testing.T) {
	t.Helper()
	old := DefaultObjDir
	unreadableDir := filepath.Join(t.TempDir(), "nope")
	require.NoError(t, os.Mkdir(unreadableDir, 0o000))
	DefaultObjDir = unreadableDir
	t.Cleanup(func() {
		DefaultObjDir = old
		_ = os.Chmod(unreadableDir, 0o700)
	})

	_, err := LoadBPF("anything.o", nil)
	require.Error(t, err)
}

func TestLoadBPF_LoadsFromDir(t *testing.T) {
	t.Helper()
	requireBPFPermission(t)

	old := DefaultObjDir
	DefaultObjDir = t.TempDir()

	t.Cleanup(func() { DefaultObjDir = old })

	objBytes := loadMinimalObjBytes(t)
	objPath := filepath.Join(DefaultObjDir, "test_minimal.elf")
	require.NoError(t, os.WriteFile(objPath, objBytes, 0o600))

	b, err := LoadBPF("test_minimal.elf", nil)
	if errors.Is(err, ebpf.ErrNotSupported) {
		t.Skipf("skipping: ebpf not supported: %v", err)
	}
	require.NoError(t, err)
	assert.Equal(t, "test_minimal.elf", b.Name())

	t.Cleanup(func() { b.Close() })
}

// TestDefaultBPF_Lifecycle_And_Accessors tests the basic lifecycle and accessor methods of defaultBPF.
//
// Covered functions:
// - Name()
// - MapIDByName(name string) uint32
// - ProgramIDByName(name string) uint32
// - String() string
// - Info() (*Info, error)
// - IsLoaded() (bool, error)
// - Close() error
func TestDefaultBPF_Lifecycle_And_Accessors(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)

	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			"Name",
			func(t *testing.T) {
				assert.Equal(t, "test_minimal.elf", b.Name())
			},
		},
		{
			"MapIDByName",
			func(t *testing.T) {
				assert.Zero(t, b.MapIDByName("non_existent_map"))
			},
		},
		{
			"ProgramIDByName",
			func(t *testing.T) {
				assert.Zero(t, b.ProgramIDByName("non_existent_prog"))
			},
		},
		{
			"String",
			func(t *testing.T) {
				str := b.String()
				expectedSubstr := fmt.Sprintf("%s#2#6", b.Name())
				assert.Contains(t, str, expectedSubstr)
			},
		},
		{
			"Info",
			func(t *testing.T) {
				info, err := b.Info()
				require.NoError(t, err)
				require.Len(t, info.MapsInfo, 2)
				require.Len(t, info.ProgramsInfo, 6)
			},
		},
		{
			"IsLoaded",
			func(t *testing.T) {
				loaded, err := b.IsLoaded()
				require.NoError(t, err)
				assert.True(t, loaded)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// TestDefaultBPF_MapOperations_Comprehensive tests all map operations including boundary conditions.
//
// Covered functions:
// - WriteMapItems(mapID uint32, items []MapItem) error
// - ReadMap(mapID uint32, key []byte) ([]byte, error)
// - DumpMap(mapID uint32) ([]MapItem, error)
// - DumpMapByName(name string) ([]MapItem, error)
// - DeleteMapItems(mapID uint32, keys [][]byte) error
func TestDefaultBPF_MapOperations(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)

	mapID := b.MapIDByName("counter_map")
	require.NotZero(t, mapID)

	makeKey := func(v uint32) []byte {
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, v)
		return buf
	}

	makeValue := func(v uint64) []byte {
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint64(buf, v)
		return buf
	}

	key := makeKey(0)
	val := makeValue(100)

	// Table-driven test cases
	tests := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{
			name: "Basic_Write",
			fn: func(t *testing.T) {
				err := b.WriteMapItems(mapID, []MapItem{{Key: key, Value: val}})
				require.NoError(t, err)
			},
		},
		{
			name: "Basic_Read",
			fn: func(t *testing.T) {
				got, err := b.ReadMap(mapID, key)
				require.NoError(t, err)
				assert.True(t, bytes.Equal(got, val))
			},
		},
		{
			name: "Basic_Dump",
			fn: func(t *testing.T) {
				items, err := b.DumpMap(mapID)
				require.NoError(t, err)
				assert.Len(t, items, 1)
			},
		},
		{
			name: "Basic_DumpByName",
			fn: func(t *testing.T) {
				items, err := b.DumpMapByName("counter_map")
				require.NoError(t, err)
				assert.Len(t, items, 1)
			},
		},
		{
			name: "Boundary_ReadNotFound",
			fn: func(t *testing.T) {
				got, err := b.ReadMap(mapID, makeKey(1))
				require.NoError(t, err)
				assert.Nil(t, got)
			},
		},
		{
			name: "Boundary_ArrayDeleteNotSupported",
			fn: func(t *testing.T) {
				err := b.DeleteMapItems(mapID, [][]byte{key})
				assert.Error(t, err)
			},
		},
		{
			name: "Error_InvalidKeySize",
			fn: func(t *testing.T) {
				err := b.WriteMapItems(mapID, []MapItem{{Key: make([]byte, 8), Value: val}})
				assert.Error(t, err)
			},
		},
		{
			name: "Error_InvalidMapID",
			fn: func(t *testing.T) {
				err := b.WriteMapItems(99999, []MapItem{{Key: key, Value: val}})
				require.ErrorIs(t, err, ErrMapNotFound)
				assert.EqualError(t, err, "bpf: map not found: id 99999")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.fn)
	}
}

func TestDefaultBPF_DumpPerCPUMap(t *testing.T) {
	requireBPFPermission(t)

	m, err := ebpf.NewMap(&ebpf.MapSpec{
		Type:       ebpf.PerCPUArray,
		KeySize:    4,
		ValueSize:  8,
		MaxEntries: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })

	possibleCPUs, err := ebpf.PossibleCPU()
	require.NoError(t, err)

	want := make([]uint64, possibleCPUs)
	for cpu := range want {
		want[cpu] = uint64(cpu + 1)
	}
	require.NoError(t, m.Put(uint32(0), want))

	const mapID = uint32(1)
	b := &defaultBPF{
		mapsByID: map[uint32]loadedMap{
			mapID: {name: "per_cpu", handle: m},
		},
	}

	items, err := b.DumpMap(mapID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, []byte{0, 0, 0, 0}, items[0].Key)

	got := make([]uint64, possibleCPUs)
	require.NoError(t, binary.Read(bytes.NewReader(items[0].Value), binary.LittleEndian, &got))
	assert.Equal(t, want, got)
}

func TestDefaultBPFDumpMapBatch(t *testing.T) {
	requireBPFPermission(t)
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
					requireBPFMapBatch(t, m)

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

					object := &defaultBPF{mapsByID: map[uint32]loadedMap{1: {handle: m}}}
					items, err := DumpMapBatch(object, 1)
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

func TestDefaultBPFDumpMapBatchPropagatesErrno(t *testing.T) {
	requireBPFPermission(t)
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
			requireBPFMapBatch(t, m)

			t.Run("permission", func(t *testing.T) {
				writeOnly, err := ebpf.NewMap(&ebpf.MapSpec{
					Type: mapType, KeySize: 40, ValueSize: 8, MaxEntries: 2,
					Flags: flags | unix.BPF_F_WRONLY,
				})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, writeOnly.Close()) })
				object := &defaultBPF{mapsByID: map[uint32]loadedMap{1: {handle: writeOnly}}}
				items, err := DumpMapBatch(object, 1)
				require.ErrorIs(t, err, unix.EPERM)
				require.Nil(t, items)
			})

			t.Run("bad fd", func(t *testing.T) {
				require.NoError(t, m.Close())
				object := &defaultBPF{mapsByID: map[uint32]loadedMap{1: {handle: m}}}
				items, err := DumpMapBatch(object, 1)
				require.ErrorIs(t, err, unix.EBADF)
				require.Nil(t, items)
			})
		})
	}
}

func TestDefaultBPFDumpMapBatchConcurrentPerCPUUpdates(t *testing.T) {
	requireBPFPermission(t)
	m, err := ebpf.NewMap(&ebpf.MapSpec{
		Type: ebpf.PerCPUHash, KeySize: 40, ValueSize: 8, MaxEntries: 2,
		Flags: unix.BPF_F_NO_PREALLOC,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	requireBPFMapBatch(t, m)
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

	object := &defaultBPF{mapsByID: map[uint32]loadedMap{1: {handle: m}}}
	for snapshot := 0; snapshot < 128; {
		require.NoError(t, ctx.Err(), "concurrent batch collection exceeded its deadline")
		items, err := DumpMapBatch(object, 1)
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

// TestDefaultBPF_Attach_SpecTypes tests the Attach function with various program types.
//
// Covered functions:
// - Attach() error
// - attachTracepoint(opts tracepointAttachOptions) error
// - attachKprobe(opts kprobeAttachOptions) error
// - attachRawTracepoint(opts rawTracepointAttachOptions) error
func TestDefaultBPF_Attach(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)

	// first Attach, success
	if err := b.Attach(); err != nil {
		t.Errorf("Attach() failed on first call: %v", err)
	} else {
		t.Log("Attach() succeeded on first call")
	}

	// second Attach，return error（repeat attach）
	if err := b.Attach(); err == nil {
		t.Errorf("Attach() expected error on second call (duplicate attach), got nil")
	} else {
		t.Logf("Got expected error on second Attach: %v", err)
	}
}

// TestDefaultBPF_AttachWithOptions_SpecTypes tests AttachWithOptions with various options.
//
// Covered functions:
// - AttachWithOptions(opts []AttachOption) error
// - attachPerfEvent(progID uint32, samplePeriod, sampleFreq uint64) error
func TestDefaultBPF_AttachWithOptions_SpecTypes(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)
	defer b.Close()

	tests := []struct {
		name     string
		progName string
		symbol   string
		perfOpt  *struct {
			SamplePeriod uint64
			SampleFreq   uint64
			CPUIDs       []int
			Type         uint32
			Config       uint64
		}
		wantErr bool
	}{
		{
			name:     "Kprobe attach",
			progName: "test_kprobe",
			symbol:   "sys_openat",
			wantErr:  false,
		},
		{
			name:     "Kretprobe attach",
			progName: "test_kretprobe",
			symbol:   "sys_openat",
			wantErr:  false,
		},
		{
			name:     "Tracepoint attach",
			progName: "eventpipe_prog",
			symbol:   "syscalls/sys_enter_nanosleep",
			wantErr:  false,
		},
		{
			name:     "PerfEvent attach (valid freq)",
			progName: "perf_event_prog",
			symbol:   "syscalls/sys_enter_getpid",
			perfOpt: &struct {
				SamplePeriod uint64
				SampleFreq   uint64
				CPUIDs       []int
				Type         uint32
				Config       uint64
			}{
				SampleFreq: 99,
				Type:       unix.PERF_TYPE_SOFTWARE,
				Config:     unix.PERF_COUNT_SW_CPU_CLOCK,
			},
			wantErr: false,
		},
		{
			name:     "PerfEvent attach (invalid freq)",
			progName: "eventpipe_prog",
			symbol:   "syscalls/sys_enter_nanosleep",
			perfOpt: &struct {
				SamplePeriod uint64
				SampleFreq   uint64
				CPUIDs       []int
				Type         uint32
				Config       uint64
			}{SampleFreq: 0},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			opts := []AttachOption{
				{
					ProgramName: tt.progName,
					Symbol:      tt.symbol,
				},
			}
			if tt.perfOpt != nil {
				opts[0].PerfEvent = *tt.perfOpt
			}

			err := b.AttachWithOptions(opts)

			// Skip test if perf_event attach lacks permission
			// in container/CI environments
			if err != nil && isPerfEventUnavailable(err) {
				t.Skipf("skipping: perf_event not available: %v", err)
			}

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				} else {
					t.Logf("got expected error: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("AttachWithOptions() returned unexpected error: %v", err)
				}
			}
		})
	}
}

func TestDefaultBPF_AttachIndependentlyPreservesEarlierLink(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)
	defer b.Close()

	const programName = "eventpipe_prog"
	err := AttachIndependently(b, &AttachOption{
		ProgramName: programName,
		Symbol:      "syscalls/sys_enter_nanosleep",
	})
	require.NoError(t, err)

	progID := b.ProgramIDByName(programName)
	program, ok := b.programsByID[progID]
	require.True(t, ok)
	require.Len(t, program.links, 1)

	err = AttachIndependently(b, &AttachOption{
		// Use a loaded program so failure comes from the attach helper,
		// not the unknown-program guard before the rollback boundary.
		ProgramName: "test_kprobe",
		Symbol:      "huatuo_test_nonexistent_kprobe_target",
	})
	require.Error(t, err)
	require.Len(t, program.links, 1, "failed independent attach must not roll back an earlier link")
	// Detach closes links without removing their map entries. Check the
	// underlying perf event after failure, not just the retained entry.
	for _, attached := range program.links {
		perfEvent, ok := attached.(link.PerfEvent)
		require.True(t, ok)
		file, err := perfEvent.PerfEvent()
		require.NoError(t, err, "earlier link must remain open")
		require.NoError(t, file.Close())
	}
}

func TestDefaultBPF_DetachProgramPreservesOtherLinks(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)
	defer b.Close()

	require.NoError(t, AttachIndependently(b, &AttachOption{
		ProgramName: "eventpipe_prog",
		Symbol:      "syscalls/sys_enter_nanosleep",
	}))
	require.NoError(t, AttachIndependently(b, &AttachOption{
		ProgramName: "test_kprobe",
		Symbol:      "sys_openat",
	}))

	require.NoError(t, DetachProgram(b, "eventpipe_prog"))
	require.Empty(t, b.programsByID[b.ProgramIDByName("eventpipe_prog")].links)
	require.Len(t, b.programsByID[b.ProgramIDByName("test_kprobe")].links, 1)
}

// TestDefaultBPF_EventPipe_Flow tests the event pipe functionality.
//
// Covered functions:
// - EventPipe(ctx context.Context, mapID uint32, perCPUBufSize int) (*PerfEventReader, error)
// - EventPipeByName(ctx context.Context, mapName string, perCPUBufSize int) (*PerfEventReader, error)
// - AttachAndEventPipe(ctx context.Context, mapName string, perCPUBufSize int) (*PerfEventReader, error)
func TestDefaultBPF_EventPipe_Flow(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)

	mapName := "events"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	tests := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{
			name: "EventPipe",
			fn: func(t *testing.T) {
				mapID := b.MapIDByName(mapName)
				reader, err := b.EventPipe(ctx, mapID, 1024)
				if err != nil {
					t.Errorf("EventPipe() error = %v", err)
				}
				reader.Close()
			},
		},
		{
			name: "EventPipeByName",
			fn: func(t *testing.T) {
				reader, err := b.EventPipeByName(ctx, mapName, 1024)
				if err != nil {
					t.Errorf("EventPipeByName() error = %v", err)
				}
				reader.Close()
			},
		},
		{
			name: "AttachAndEventPipe",
			fn: func(t *testing.T) {
				reader, err := b.AttachAndEventPipe(ctx, mapName, 1024)
				if err != nil {
					t.Errorf("AttachAndEventPipe() error (might be expected): %v", err)
				} else {
					t.Log("AttachAndEventPipe() succeeded")
					reader.Close()
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.fn)
	}
}

func TestDefaultBPF_DetachOnContextDone(t *testing.T) {
	b := &defaultBPF{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.DetachOnContextDone(ctx, cancel)
}

func loadMinimalObjBytes(t *testing.T) []byte {
	t.Helper()

	objBytes, err := os.ReadFile(
		testutils.NativeFile(t, "../../integration/testdata/test_minimal-%s.elf"),
	)
	require.NoError(t, err)

	return objBytes
}

func loadMinimalBpfFromBytes(t *testing.T) *defaultBPF {
	t.Helper()
	requireBPFPermission(t)

	objBytes := loadMinimalObjBytes(t)
	obj, err := LoadBPFFromBytes("test_minimal.elf", objBytes, nil)
	if errors.Is(err, ebpf.ErrNotSupported) {
		t.Skipf("skipping: ebpf not supported: %v", err)
	}
	require.NoError(t, err)

	b, ok := obj.(*defaultBPF)
	require.True(t, ok, "expected *defaultBPF, got %T", obj)

	t.Cleanup(func() { b.Close() })

	return b
}

// requireBPFPermission skips the test when the process lacks CAP_BPF, probed
// via a tiny map create. Keeps unprivileged environments (containers, CI) from
// failing with EPERM on every BPF-loading test.
func requireBPFPermission(tb testing.TB) {
	tb.Helper()

	m, err := ebpf.NewMap(&ebpf.MapSpec{
		Type:       ebpf.Hash,
		KeySize:    4,
		ValueSize:  4,
		MaxEntries: 1,
	})
	if err != nil {
		if errors.Is(err, ebpf.ErrNotSupported) ||
			errors.Is(err, unix.EPERM) ||
			errors.Is(err, unix.EACCES) {
			tb.Skipf("insufficient permissions for bpf: %v", err)
		}
		tb.Fatalf("ebpf.NewMap() = %v, want nil", err)
	}
	_ = m.Close()
}

func requireBPFMapBatch(t *testing.T, m *ebpf.Map) {
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

// isPerfEventUnavailable returns true if attaching a perf event is not allowed
// due to permission issues, container limitations, or unsupported environment.
//
// It checks two things:
// 1. The kernel setting /proc/sys/kernel/perf_event_paranoid: values >1 block perf_event for non-root users.
// 2. The actual error from attach operations, e.g., "invalid argument" or "permission denied".
func isPerfEventUnavailable(err error) bool {
	// Check perf_event_paranoid
	data, readErr := os.ReadFile("/proc/sys/kernel/perf_event_paranoid")
	if readErr == nil {
		val := strings.TrimSpace(string(data))
		if v, convErr := strconv.Atoi(val); convErr == nil && v > 1 {
			return true
		}
	}

	// Check runtime error from perf attach
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "invalid argument") || strings.Contains(lower, "permission denied") {
		return true
	}

	return false
}

func TestDefaultBPF_CloseOrder(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)

	err := b.AttachWithOptions([]AttachOption{
		{ProgramName: "test_kprobe", Symbol: "sys_openat"},
	})
	if errors.Is(err, ebpf.ErrNotSupported) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
		t.Skipf("skipping: attach not supported: %v", err)
	}
	require.NoError(t, err)

	hasLinks := false
	for _, p := range b.programsByID {
		if len(p.links) > 0 {
			hasLinks = true
			break
		}
	}
	require.True(t, hasLinks, "expected at least one link after attach")

	closeErr := b.Close()
	require.NoError(t, closeErr, "Close after attach should not return EBUSY; wrong close order would cause kernel to refuse map/program close while links are active")
}

func TestDefaultBPF_DetachOrder(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)

	err := b.AttachWithOptions([]AttachOption{
		{ProgramName: "test_kprobe", Symbol: "sys_openat"},
	})
	if errors.Is(err, ebpf.ErrNotSupported) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
		t.Skipf("skipping: attach not supported: %v", err)
	}
	require.NoError(t, err)

	hasLinks := false
	for _, p := range b.programsByID {
		if len(p.links) > 0 {
			hasLinks = true
			break
		}
	}
	require.True(t, hasLinks, "expected at least one link after attach")

	detachErr := b.Detach()
	require.NoError(t, detachErr, "Detach should only close links and perf event, not programs or maps")

	noLinks := true
	for _, p := range b.programsByID {
		if len(p.links) > 0 {
			noLinks = false
			break
		}
	}
	assert.True(t, noLinks, "Detach should clear all links")
}

func TestDefaultBPF_Close_Idempotent(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)

	require.NoError(t, b.Close())
	require.NoError(t, b.Close())

	loaded, err := b.IsLoaded()
	require.NoError(t, err)
	require.False(t, loaded)
}

func TestDefaultBPF_Detach_AfterClose(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)

	require.NoError(t, b.Close())
	require.NoError(t, b.Detach())
}

func TestDefaultBPF_OperationsAfterClose(t *testing.T) {
	b := loadMinimalBpfFromBytes(t)
	mapID := b.MapIDByName("counter_map")
	require.NotZero(t, mapID)
	require.NoError(t, b.Close())

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "info",
			run: func() error {
				_, err := b.Info()
				return err
			},
		},
		{
			name: "attach",
			run:  b.Attach,
		},
		{
			name: "attach with options",
			run: func() error {
				return b.AttachWithOptions(nil)
			},
		},
		{
			name: "event pipe",
			run: func() error {
				_, err := b.EventPipe(t.Context(), mapID, 1)
				return err
			},
		},
		{
			name: "event pipe by name",
			run: func() error {
				_, err := b.EventPipeByName(t.Context(), "counter_map", 1)
				return err
			},
		},
		{
			name: "attach and event pipe",
			run: func() error {
				_, err := b.AttachAndEventPipe(t.Context(), "counter_map", 1)
				return err
			},
		},
		{
			name: "read map",
			run: func() error {
				_, err := b.ReadMap(mapID, make([]byte, 4))
				return err
			},
		},
		{
			name: "write map",
			run: func() error {
				return b.WriteMapItems(mapID, nil)
			},
		},
		{
			name: "delete map",
			run: func() error {
				return b.DeleteMapItems(mapID, nil)
			},
		},
		{
			name: "dump map",
			run: func() error {
				_, err := b.DumpMap(mapID)
				return err
			},
		},
		{
			name: "dump map by name",
			run: func() error {
				_, err := b.DumpMapByName("counter_map")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, tt.run(), ErrClosed)
		})
	}
}

func TestDefaultBPF_CloseWaitsForOperation(t *testing.T) {
	b := &defaultBPF{}
	b.mu.RLock()

	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- b.Close()
	}()
	<-closeStarted

	deadline := time.Now().Add(time.Second)
	for b.mu.TryRLock() {
		b.mu.RUnlock()
		if time.Now().After(deadline) {
			b.mu.RUnlock()
			t.Fatal("Close() did not wait for the operation lock")
		}
		runtime.Gosched()
	}

	select {
	case err := <-closeDone:
		b.mu.RUnlock()
		t.Fatalf("Close() returned before the operation completed: %v", err)
	default:
	}

	b.mu.RUnlock()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close() did not return after the operation completed")
	}
}
