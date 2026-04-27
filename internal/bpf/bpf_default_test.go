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

	testutils "huatuo-bamai/internal/testing"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if runtime.GOOS != "linux" {
		fmt.Println("skipping tests: requires linux with ebpf support")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestManager_InitAndClose(t *testing.T) {
	// Just verify they don't panic.
	if err := NewManager(nil); err != nil {
		// It might fail on non-Linux or without permissions.
		t.Fatalf("InitBpfManager returned: %v", err)
	} else {
		Close()
	}
}

// TestLoad* tests the basic logic of LoadBpfFromBytes and LoadBpf.
func TestLoadBpfFromBytes_InvalidELF(t *testing.T) {
	_, err := LoadBpfFromBytes("invalid", []byte("not-an-elf"), nil)
	require.Error(t, err)
}

func TestLoadBpfFromBytes_InvalidName(t *testing.T) {
	cases := []string{
		"",
		"../x.o",
		"x/evil.o",
		"..",
		".",
		"./x.o",
		"a..b.o",
		"x/../y.o",
		"x\\evil.o",
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadBpfFromBytes(name, []byte("x"), nil)
			require.Error(t, err)
		})
	}
}

func TestLoadBpf_InvalidName(t *testing.T) {
	cases := []string{
		"",
		"../x.o",
		"x/evil.o",
		"..",
		".",
		"./x.o",
		"a..b.o",
		"x/../y.o",
		"x\\evil.o",
	}

	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadBpf(name, nil)
			require.Error(t, err)
		})
	}
}

func TestLoadBpf_FileNotFound(t *testing.T) {
	old := DefaultBpfObjDir
	DefaultBpfObjDir = t.TempDir()
	t.Cleanup(func() { DefaultBpfObjDir = old })

	_, err := LoadBpf("definitely_not_exists.o", nil)
	require.Error(t, err)
}

func TestLoadBpf_DefaultBpfObjDir_Empty(t *testing.T) {
	old := DefaultBpfObjDir
	DefaultBpfObjDir = ""
	t.Cleanup(func() { DefaultBpfObjDir = old })

	_, err := LoadBpf("definitely_not_exists.o", nil)
	require.Error(t, err)
}

func TestLoadBpf_DefaultBpfObjDir_Relative(t *testing.T) {
	old := DefaultBpfObjDir
	DefaultBpfObjDir = "./definitely_not_exists_dir"
	t.Cleanup(func() { DefaultBpfObjDir = old })

	_, err := LoadBpf("definitely_not_exists.o", nil)
	require.Error(t, err)
}

func TestLoadBpf_DefaultBpfObjDir_Unreadable(t *testing.T) {
	t.Helper()
	old := DefaultBpfObjDir
	unreadableDir := filepath.Join(t.TempDir(), "nope")
	require.NoError(t, os.Mkdir(unreadableDir, 0o000))
	DefaultBpfObjDir = unreadableDir
	t.Cleanup(func() {
		DefaultBpfObjDir = old
		_ = os.Chmod(unreadableDir, 0o700)
	})

	_, err := LoadBpf("anything.o", nil)
	require.Error(t, err)
}

func TestLoadBpf_LoadsFromDir(t *testing.T) {
	t.Helper()

	old := DefaultBpfObjDir
	DefaultBpfObjDir = t.TempDir()

	t.Cleanup(func() { DefaultBpfObjDir = old })

	objBytes := loadMinimalObjBytes(t)
	objPath := filepath.Join(DefaultBpfObjDir, "test_minimal.elf")
	require.NoError(t, os.WriteFile(objPath, objBytes, 0o600))

	b, err := LoadBpf("test_minimal.elf", nil)
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
// - ProgIDByName(name string) uint32
// - String() string
// - Info() (*Info, error)
// - Loaded() (bool, error)
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
			"ProgIDByName",
			func(t *testing.T) {
				assert.Zero(t, b.ProgIDByName("non_existent_prog"))
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
			"Loaded",
			func(t *testing.T) {
				loaded, err := b.Loaded()
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
				assert.Panics(t, func() {
					_ = b.WriteMapItems(99999, []MapItem{{Key: key, Value: val}})
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.fn)
	}
}

// TestDefaultBPF_Attach_SpecTypes tests the Attach function with various program types.
//
// Covered functions:
// - Attach() error
// - attachTracepoint(progID uint32, system, symbol string) error
// - attachKprobe(progID uint32, symbol string, isRetprobe bool) error
// - attachRawTracepoint(progID uint32, symbol string) error
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
			}{SampleFreq: 99},
			wantErr: false,
		},
		{
			name:     "PerfEvent attach (invalid freq)",
			progName: "eventpipe_prog",
			symbol:   "syscalls/sys_enter_nanosleep",
			perfOpt: &struct {
				SamplePeriod uint64
				SampleFreq   uint64
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

// TestDefaultBPF_EventPipe_Flow tests the event pipe functionality.
//
// Covered functions:
// - EventPipe(ctx context.Context, mapID uint32, channelSize int) (*PerfEventReader, error)
// - EventPipeByName(ctx context.Context, mapName string, channelSize int) (*PerfEventReader, error)
// - AttachAndEventPipe(ctx context.Context, mapName string, channelSize int) (*PerfEventReader, error)
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

func TestDefaultBPF_WaitDetachByBreaker(t *testing.T) {
	b := &defaultBPF{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.WaitDetachByBreaker(ctx, cancel)
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

	objBytes := loadMinimalObjBytes(t)
	obj, err := LoadBpfFromBytes("test_minimal.elf", objBytes, nil)
	if errors.Is(err, ebpf.ErrNotSupported) {
		t.Skipf("skipping: ebpf not supported: %v", err)
	}
	require.NoError(t, err)

	b, ok := obj.(*defaultBPF)
	require.True(t, ok, "expected *defaultBPF, got %T", obj)

	t.Cleanup(func() { b.Close() })

	return b
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
