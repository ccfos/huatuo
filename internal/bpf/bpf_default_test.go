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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	testutils "github.com/ccfos/huatuo/internal/testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
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

func TestLoadBPFFromCollectionSpec_Constants(t *testing.T) {
	tests := []struct {
		name      string
		constants map[string]any
		writable  bool
		wantError string
		wantErr   error
	}{
		{
			name:      "missing",
			constants: map[string]any{"missing": uint32(1)},
			wantError: `rewrite constant "missing": bpf: variable not found`,
			wantErr:   ErrVariableNotFound,
		},
		{
			name:      "wrong size",
			constants: map[string]any{"target_key": uint64(1)},
			wantError: "rewrite constant \"target_key\"",
		},
		{
			name:      "writable",
			constants: map[string]any{"target_key": uint32(1)},
			writable:  true,
			wantError: "variable is not read-only",
		},
		{
			name:      "read-only value",
			constants: map[string]any{"target_key": uint32(42)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ebpf.LoadCollectionSpecFromReader(bytes.NewReader(loadMinimalObjBytes(t)))
			require.NoError(t, err)
			if tt.writable {
				spec.Maps[".rodata"].Flags &^= unix.BPF_F_RDONLY_PROG
			}

			if tt.wantError == "" {
				requireBPFPermission(t)
			}

			object, err := LoadBPFFromCollectionSpec("test_minimal.elf", spec, tt.constants)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}
				require.Nil(t, object)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, object.Close()) })
			items, err := object.DumpMapByName(".rodata")
			require.NoError(t, err)
			require.Len(t, items, 1)
			offset := uint64(spec.Variables["target_key"].Offset)
			require.GreaterOrEqual(t, uint64(len(items[0].Value)), offset+4)
			require.Equal(t, uint32(42), binary.NativeEndian.Uint32(items[0].Value[offset:offset+4]))
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
				expectedSubstr := fmt.Sprintf("%s#3#6", b.Name())
				assert.Contains(t, str, expectedSubstr)
			},
		},
		{
			"Info",
			func(t *testing.T) {
				info, err := b.Info()
				require.NoError(t, err)
				// cilium/ebpf retains the read-only map for global variables.
				require.Len(t, info.MapsInfo, 3)
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

type detachTestLink struct {
	link.Link
	close func() error
}

func (l detachTestLink) Close() error {
	return l.close()
}

func TestDefaultBPF_DetachOnContextDone(t *testing.T) {
	failure := errors.New("detach failed")
	for _, tc := range []struct {
		name         string
		cancelParent bool
		detachErr    error
	}{
		{name: "parent cancellation", cancelParent: true},
		{name: "early cleanup"},
		{name: "detach error", cancelParent: true, detachErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			detached := make(chan struct{})
			b := &defaultBPF{
				programsByID: map[uint32]*loadedProgram{
					1: {
						links: map[string]link.Link{
							"test": detachTestLink{close: func() error {
								calls.Add(1)
								close(detached)
								return tc.detachErr
							}},
						},
					},
				},
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cleanup := b.DetachOnContextDone(ctx)
			t.Cleanup(func() { _ = cleanup() })
			if tc.cancelParent {
				cancel()
				select {
				case <-detached:
				case <-time.After(time.Second):
					t.Fatal("context cancellation did not detach the link")
				}
			}

			var wait sync.WaitGroup
			for range 8 {
				wait.Add(1)
				go func() {
					defer wait.Done()
					if err := cleanup(); !errors.Is(err, tc.detachErr) {
						t.Errorf("cleanup error = %v, want %v", err, tc.detachErr)
					}
				}()
			}
			wait.Wait()
			require.Equal(t, int32(1), calls.Load())
			require.NoError(t, b.Close())
			require.ErrorIs(t, cleanup(), tc.detachErr)
		})
	}

	t.Run("cleanup waits for detachment", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		b := &defaultBPF{
			programsByID: map[uint32]*loadedProgram{
				1: {
					links: map[string]link.Link{
						"test": detachTestLink{close: func() error {
							close(entered)
							<-release
							return nil
						}},
					},
				},
			},
		}
		cleanup := b.DetachOnContextDone(t.Context())
		joined := make(chan error, 1)
		go func() { joined <- cleanup() }()
		t.Cleanup(func() {
			close(release)
			require.NoError(t, cleanup())
		})
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("cleanup did not start detachment")
		}
		select {
		case err := <-joined:
			t.Fatalf("cleanup returned before detachment completed: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	})

	t.Run("close races cancellation", func(t *testing.T) {
		var calls atomic.Int32
		b := &defaultBPF{
			programsByID: map[uint32]*loadedProgram{
				1: {
					links: map[string]link.Link{
						"test": detachTestLink{close: func() error {
							calls.Add(1)
							return nil
						}},
					},
				},
			},
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cleanup := b.DetachOnContextDone(ctx)
		t.Cleanup(func() { require.NoError(t, cleanup()) })
		closed := make(chan error, 1)
		go func() { closed <- b.Close() }()
		cancel()
		require.NoError(t, cleanup())
		require.NoError(t, <-closed)
		require.Equal(t, int32(1), calls.Load())
	})

	t.Run("closed object", func(t *testing.T) {
		b := &defaultBPF{}
		require.NoError(t, b.Close())
		require.NoError(t, b.DetachOnContextDone(t.Context())())
	})

	t.Run("maps survive detachment", func(t *testing.T) {
		b := loadMinimalBpfFromBytes(t)
		err := b.AttachWithOptions([]AttachOption{{
			ProgramName: "test_kprobe",
			Symbol:      "sys_openat",
		}})
		if err != nil && (isPerfEventUnavailable(err) || errors.Is(err, link.ErrNotSupported)) {
			t.Skipf("kprobe attachment is unavailable: %v", err)
		}
		require.NoError(t, err)
		mapID := b.MapIDByName("counter_map")
		cleanup := b.DetachOnContextDone(t.Context())
		t.Cleanup(func() { require.NoError(t, cleanup()) })
		require.NoError(t, cleanup())
		for _, program := range b.programsByID {
			require.Empty(t, program.links)
		}
		_, err = b.DumpMap(mapID)
		require.NoError(t, err)
		require.NoError(t, b.Close())
		_, err = b.DumpMap(mapID)
		require.ErrorIs(t, err, ErrClosed)
	})
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
	require.NoError(t, closeErr, "Close after attach should succeed")
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
			name: "raw event pipe",
			run: func() error {
				_, err := b.RawEventPipe(
					t.Context(),
					mapID,
					PerfEventReaderOptions{
						PerCPUBufferBytes: 4096,
						WatermarkBytes:    1,
					},
				)
				return err
			},
		},
		{
			name: "raw event pipe by name",
			run: func() error {
				_, err := b.RawEventPipeByName(
					t.Context(),
					"counter_map",
					PerfEventReaderOptions{
						PerCPUBufferBytes: 4096,
						WatermarkBytes:    1,
					},
				)
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

var firstLoadBenchmarkStarted atomic.Bool

// BenchmarkLoadBPFFirst measures the production loader without attaching BPF.
// Use -run=^$, -benchtime=1x and -count=1 in a new process for each sample.
func BenchmarkLoadBPFFirst(b *testing.B) {
	// Go 1.24 may exit successfully if a later repeated benchmark fails.
	if flag.Lookup("test.count").Value.String() != "1" ||
		strings.Contains(flag.Lookup("test.cpu").Value.String(), ",") {
		b.Fatal("first-load benchmark requires -count=1 and a single -cpu value; run a new process per sample")
	}

	if !firstLoadBenchmarkStarted.CompareAndSwap(false, true) {
		b.Fatal("first-load benchmark requires a new process per sample; use -count=1")
	}

	objectPath := testutils.NativeFile(b, "../../integration/testdata/test_btf_load-%s.elf")
	oldDir := DefaultObjDir
	DefaultObjDir = filepath.Dir(objectPath)
	b.Cleanup(func() { DefaultObjDir = oldDir })
	if err := Init(nil); err != nil {
		b.Fatalf("initialize BPF loader: %v", err)
	}
	b.Cleanup(Shutdown)

	// Earlier tests must not turn this into a cached kernel-BTF lookup.
	btf.FlushKernelSpec()
	b.Cleanup(btf.FlushKernelSpec)

	var cpuNS int64
	ran := false
	b.ReportAllocs()
	for b.Loop() {
		if ran {
			b.Fatal("first-load benchmark requires -benchtime=1x")
		}
		ran = true

		var before, after syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &before); err != nil {
			b.Fatalf("read CPU time before BPF load: %v", err)
		}
		object, err := LoadBPF(filepath.Base(objectPath), nil)
		statErr := syscall.Getrusage(syscall.RUSAGE_SELF, &after)
		b.StopTimer()
		if object != nil {
			b.Cleanup(func() {
				if err := object.Close(); err != nil {
					b.Errorf("close benchmark BPF object: %v", err)
				}
			})
		}
		if statErr != nil {
			b.Fatalf("read CPU time after BPF load: %v", statErr)
		}
		if err != nil {
			b.Fatalf("load %s: %v", objectPath, err)
		}
		if object == nil {
			b.Fatal("BPF loader returned no object")
		}
		cpuNS = after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano()
		if cpuNS <= 0 {
			b.Fatalf("BPF load reported invalid CPU time: %d ns", cpuNS)
		}

		info, err := object.Info()
		if err != nil {
			b.Fatalf("inspect benchmark BPF object: %v", err)
		}
		if len(info.ProgramsInfo) != 1 || len(info.MapsInfo) != 0 {
			b.Fatal("BTF fixture must load one kernel program and no maps")
		}
		for _, program := range info.ProgramsInfo {
			if program.ID == 0 {
				b.Fatalf("program %q has no kernel ID", program.Name)
			}
		}
		requireBenchmarkCOREObject(b, objectPath)
		b.StartTimer()
	}
	b.ReportMetric(float64(cpuNS), "cpu-ns/op")
}

// A load without CO-RE could succeed without exercising kernel BTF decoding.
// Inspect only after measurement so input validation cannot warm loader caches.
func requireBenchmarkCOREObject(b *testing.B, path string) {
	b.Helper()

	spec, err := ebpf.LoadCollectionSpec(path)
	if err != nil {
		b.Fatalf("inspect benchmark ELF: %v", err)
	}
	program, ok := spec.Programs["minimal_core"]
	if !ok {
		b.Fatal("benchmark ELF has no minimal CO-RE program")
	}

	iter := program.Instructions.Iterate()
	for iter.Next() {
		if btf.CORERelocationMetadata(iter.Ins) != nil {
			return
		}
	}
	b.Fatal("benchmark fixture program has no CO-RE relocations")
}
