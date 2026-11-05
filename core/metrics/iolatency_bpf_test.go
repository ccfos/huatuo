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

// IOLatency Go startup, compatibility selection, and disk discovery tests.
package collector

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/internal/utils/netutil"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf/btf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Compatibility: raw tracepoints and required hooks.

type fakeIOLatencyAttacher struct {
	bpf.BPF
	attached  [][]string
	options   []bpf.AttachOption
	fail      map[string]error
	detached  []string
	detachErr error
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

func (b *fakeIOLatencyAttacher) Detach() error {
	b.detached = append(b.detached, "all")
	return b.detachErr
}

// Argument probes distinguish compatibility boundaries from errors.

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
			if test.attachErr == nil && !slices.Equal(object.detached, []string{"all"}) {
				t.Fatalf("attached probe was not released: %v", object.detached)
			}
		})
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
	var programs []string
	hooks, err := ioControlDeviceHooks()
	require.NoError(t, err)
	for _, hook := range hooks {
		programs = append(programs, hook.ProgramName)
	}
	programs = append(
		programs,
		"probe_queue_stats", "io_latency_css_released",
	)
	for _, definition := range ioLatencyTracepoints {
		programs = append(programs, definition.program)
	}
	object := &fakeIOLatencyAttacher{}
	require.NoError(t, attachIOLatencyHooks(object))
	require.Equal(t, [][]string{programs}, object.attached)
	for _, program := range programs {
		t.Run(program, func(t *testing.T) {
			failure := errors.New("attach failed")
			object := &fakeIOLatencyAttacher{fail: map[string]error{program: failure}}
			require.ErrorIs(t, attachIOLatencyHooks(object), failure)
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

func TestIOLatencyTracepointArgumentSelection(t *testing.T) {
	for layout := range 8 {
		t.Run(fmt.Sprintf("layout-%03b", layout), func(t *testing.T) {
			counts := map[string]uint32{
				"block_bio_queue": uint32(1 + (layout & 1)),
				"block_bio_remap": uint32(3 + ((layout >> 1) & 1)),
				"block_rq_remap":  uint32(3 + ((layout >> 2) & 1)),
			}
			constants, err := ioLatencyTracepointArguments(func(symbol string, argument uint32) (bool, error) {
				return argument < counts[symbol], nil
			})
			require.NoError(t, err)
			assertIOLatencyConstant(
				t,
				constants,
				ioLatencyQueueBioArgument,
				uint32(layout&1),
			)
			assertIOLatencyConstant(
				t,
				constants,
				ioLatencyRemapBioArgument,
				uint32((layout>>1)&1),
			)
			assertIOLatencyConstant(
				t,
				constants,
				ioLatencyRemapRequestArgument,
				uint32((layout>>2)&1),
			)
		})
	}
}

// Startup admission and BTF qualification.

func TestIOLatencyKernelStartup(t *testing.T) {
	for _, test := range []struct {
		name        string
		symbols     string
		config      string
		hostConfig  string
		localConfig string
		want        string
		root        uint64
		bioFlag     uint32
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
			name:       "container reads host boot config without proc config",
			hostConfig: "# CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK is not set\n",
		},
		{
			name:        "container config cannot disable host fallback",
			hostConfig:  "CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK=y\n",
			localConfig: "# CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK is not set\n",
			want:        "tfms_inited",
		},
		{
			name:        "container config cannot replace missing host config",
			localConfig: "# CONFIG_BLK_INLINE_ENCRYPTION_FALLBACK is not set\n",
			want:        "kernel config",
		},
		{
			name:    "root blkcg address is a startup constant",
			symbols: "ffffffff82003000 B blkcg_root\n",
			config:  "CONFIG_BLOCK=y\n",
			root:    0xffffffff82003000,
		},
		{
			name:    "anonymous bio flag is a startup constant",
			config:  "CONFIG_BLOCK=y\n",
			bioFlag: 6,
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
			kernelTypes := []btf.Type{word}
			if test.bioFlag != 0 {
				kernelTypes = append(kernelTypes, &btf.Enum{
					Size:   4,
					Values: []btf.EnumValue{{Name: "BIO_TRACE_COMPLETION", Value: uint64(test.bioFlag)}},
				})
			}
			writeIOLatencyKernelBTF(t, kernelTypes)
			if err := os.MkdirAll(procfs.DefaultPath(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(procfs.Path("kallsyms"), []byte(test.symbols), 0o600); err != nil {
				t.Fatal(err)
			}
			if test.hostConfig != "" || test.localConfig != "" {
				const release = "test-kernel"
				require.NoError(t, os.MkdirAll(procfs.Path("sys", "kernel"), 0o700))
				require.NoError(t, os.WriteFile(procfs.Path("sys", "kernel", "osrelease"), []byte(release+"\n"), 0o600))
				for path, config := range map[string]string{
					procfs.Path("1", "root", "boot", "config-"+release): test.hostConfig,
					filepath.Join(root, "boot", "config-"+release):      test.localConfig,
				} {
					if config != "" {
						require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
						require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
					}
				}
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
			constants, err := loadIOLatencyKernelConstants()
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
			flag := test.bioFlag
			if flag == 0 {
				flag = 10
			}
			assertIOLatencyConstant(t, constants, ioLatencyBioTraceFlag, flag)
		})
	}
}

// Invalid BTF cannot provide the structure relocations required by IO tracing.
func TestIOLatencyKernelBTFStartup(t *testing.T) {
	word := &btf.Int{Name: "unsigned long", Size: 8, Encoding: btf.Unsigned}
	for _, test := range []struct {
		name      string
		types     []btf.Type
		missing   bool
		malformed bool
		want      string
		cause     error
	}{
		{name: "missing BTF", missing: true, want: "sys/kernel/btf/vmlinux", cause: os.ErrNotExist},
		{name: "malformed BTF", types: []btf.Type{word}, malformed: true, want: "sys/kernel/btf/vmlinux"},
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
				t.Fatal("invalid kernel BTF published a session")
			}
		})
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

func TestIOLatencyBioTraceCompletion(t *testing.T) {
	for _, test := range []struct {
		name     string
		values   []uint64
		enumName string
		want     uint32
		invalid  bool
	}{
		{name: "legacy macro ABI", want: 10},
		{name: "anonymous flag eight", values: []uint64{8}, want: 8},
		{name: "anonymous flag six", values: []uint64{6}, want: 6},
		{name: "named enum", values: []uint64{7}, enumName: "bio_flags", want: 7},
		{name: "first flag", values: []uint64{0}, want: 0},
		{name: "last flag", values: []uint64{15}, want: 15},
		{name: "equivalent enums", values: []uint64{8, 8}, want: 8},
		{name: "conflicting enums", values: []uint64{8, 6}, invalid: true},
		{name: "flag exceeds bi_flags", values: []uint64{16}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidates := []btf.Type{&btf.Enum{
				Size:   4,
				Values: []btf.EnumValue{{Name: "UNRELATED_FLAG", Value: 1}},
			}}
			for index, value := range test.values {
				candidates = append(candidates, &btf.Enum{
					Name: test.enumName, Size: 4,
					Values: []btf.EnumValue{
						{Name: "BIO_TRACE_COMPLETION", Value: value},
						{Name: "UNRELATED_FLAG", Value: uint64(index)},
					},
				})
			}
			builder, err := btf.NewBuilder(candidates)
			require.NoError(t, err)
			raw, err := builder.Marshal(nil, nil)
			require.NoError(t, err)
			spec, err := btf.LoadSpecFromReader(bytes.NewReader(raw))
			require.NoError(t, err)
			flag, err := ioLatencyBioTraceCompletion(spec)
			if test.invalid {
				require.ErrorContains(t, err, "BIO_TRACE_COMPLETION")
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, flag)
		})
	}
}

// Software blk-crypto prerequisite decoding.

func TestIOLatencyCryptoModeCount(t *testing.T) {
	for _, test := range []struct {
		name       string
		bounds     []uint64
		isReversed bool
		want       uint32
	}{
		{name: "four modes", bounds: []uint64{4}, want: 4},
		{name: "five modes", bounds: []uint64{5}, want: 5},
		{name: "missing bound"},
		{name: "empty array", bounds: []uint64{0}},
		{name: "array exceeds probe capacity", bounds: []uint64{65}},
		{name: "equivalent enums", bounds: []uint64{4, 4}, want: 4},
		{name: "equivalent enums reversed", bounds: []uint64{4, 4}, isReversed: true, want: 4},
		{name: "conflicting enums", bounds: []uint64{4, 5}},
		{name: "conflicting enums reversed", bounds: []uint64{5, 4}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidates := []btf.Type{}
			for index, bound := range test.bounds {
				// Compilation units may disagree on values unrelated to the bound.
				candidates = append(candidates, &btf.Enum{
					Name: "blk_crypto_mode_num", Size: 4,
					Values: []btf.EnumValue{
						{Name: "BLK_ENCRYPTION_MODE_MAX", Value: bound},
						{Name: "other", Value: uint64(index)},
					},
				})
			}
			if len(candidates) == 0 {
				candidates = append(candidates, &btf.Enum{
					Name: "blk_crypto_mode_num", Size: 4,
					Values: []btf.EnumValue{{Name: "BLK_ENCRYPTION_MODE_INVALID", Value: 0}},
				})
			}
			if test.isReversed {
				slices.Reverse(candidates)
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

type fakeIOLatencyCryptoBPF struct {
	bpf.BPF
	result    int64
	attachErr error
	readErr   error
}

// Queue timestamps and disk enrollment isolation.

// Disks without both kernel timestamps are excluded, not startup failures.
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
		{name: "virtio disk", disk: "vda", iostats: "1\n", result: 2},
		{name: "nonzero virtio iostats", disk: "vda", iostats: "2\n", result: 2},
		{name: "virtio extended disk name", disk: "vdaa", iostats: "1\n", result: 2},
		{name: "virtio G disabled", disk: "vda", iostats: "0\n", result: 2},
		{name: "virtio D disabled", disk: "vda", iostats: "1\n", result: 3},
		{name: "G disabled", iostats: "0\n", result: 2},
		{name: "D disabled", iostats: "1\n", result: 3},
		{name: "probe missed", iostats: "1\n", want: "did not execute"},
		{name: "failed kernel read", iostats: "1\n", result: -int64(unix.EFAULT), want: "bad address"},
		{name: "device mapper is not probed", disk: "dm-0", skip: true},
		{name: "MD is not probed", disk: "md0", skip: true},
		{name: "loop is not probed", disk: "loop0", skip: true},
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
			err := session.discoverDisks(root)
			devices := session.devices
			if test.skip {
				if err != nil || len(devices) != 0 || len(object.keys) != 0 {
					t.Fatalf("excluded disk: devices=%v, requests=%v, error=%v", devices, object.keys, err)
				}
				return
			}
			if string(bytes.TrimRight(object.keys[0], "\x00")) != disk {
				t.Fatalf("probe requested disk %q", object.keys[0])
			}
			if object.resultArmed {
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
			if test.result == 1 || test.result == 3 || test.iostats == "0\n" {
				if len(devices) != 0 || len(object.host) != 0 || len(object.keys) != 1 {
					t.Fatalf("skipped disk: devices=%v, keys=%v", devices, object.keys)
				}
				return
			}
			require.Equal(t, map[[2]uint32]string{{259, 7}: disk}, devices)
		})
	}
}

type fakeIOLatencyQueueBPF struct {
	bpf.BPF
	result      int64
	keys        [][]byte
	readErr     error
	writeErr    error
	deleteErr   error
	resultKey   []byte
	resultArmed bool
	host        map[uint64]ioLatencyDiskCounters
}

func (b *fakeIOLatencyQueueBPF) MapIDByName(name string) uint32 {
	if name == ioLatencyProbeResultMap {
		return 2
	}
	if name == blkDiskBucketMap {
		return 3
	}
	return 0
}

func (b *fakeIOLatencyQueueBPF) WriteMapItems(id uint32, items []bpf.MapItem) error {
	if id == 3 {
		if b.host == nil {
			b.host = make(map[uint64]ioLatencyDiskCounters)
		}
		for _, item := range items {
			var value ioLatencyDiskCounters
			if err := decodeBPFMapData(item.Value, &value); err != nil {
				return err
			}
			b.host[binary.NativeEndian.Uint64(item.Key)] = value
		}
		return nil
	}
	if id != 2 {
		return fmt.Errorf("unexpected disk probe result map ID %d", id)
	}
	if b.writeErr != nil {
		return b.writeErr
	}
	b.resultKey = bytes.Clone(items[0].Key)
	b.keys = append(b.keys, bytes.Clone(items[0].Key))
	b.resultArmed = true
	return nil
}

func (b *fakeIOLatencyQueueBPF) DeleteMapItems(id uint32, keys [][]byte) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	if id != 2 || !b.resultArmed || !bytes.Equal(keys[0], b.resultKey) {
		return unix.ENOENT
	}
	b.resultArmed = false
	return nil
}

func (b *fakeIOLatencyQueueBPF) ReadMap(id uint32, key []byte) ([]byte, error) {
	if id == 3 {
		value, found := b.host[binary.NativeEndian.Uint64(key)]
		if !found {
			return nil, nil
		}
		return bytesutil.ToBytes(value), nil
	}
	if id != 2 || !b.resultArmed || !bytes.Equal(key, b.resultKey) {
		return nil, unix.ENOENT
	}
	minor := uint32(7)
	switch string(bytes.TrimRight(key, "\x00")) {
	case "sdb":
		minor = 8
	case "nvme1n1":
		minor = 9
	case "vdb":
		minor = 10
	}
	return bytesutil.ToBytes(ioLatencyQueueProbe{
		Result: b.result, Major: 259, Minor: minor, Disk: uint64(minor),
	}), b.readErr
}

func TestIOLatencyQueueProbeDiskIsolation(t *testing.T) {
	root := t.TempDir()
	for _, disk := range []string{"sda", "sdb", "vda", "vdb", "nvme1n1"} {
		path := filepath.Join(root, "block", disk, "queue")
		require.NoError(t, os.MkdirAll(path, 0o700))
		stats := "1\n"
		if disk == "vda" {
			stats = "0\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(path, "iostats"), []byte(stats), 0o600))
	}
	object := &fakeIOLatencyQueueBPF{result: 2}
	session := &ioLatencySession{object: object}
	require.NoError(t, session.discoverDisks(root))
	require.False(t, object.resultArmed)
	require.Equal(t, map[[2]uint32]string{
		{259, 7}: "sda", {259, 8}: "sdb", {259, 9}: "nvme1n1", {259, 10}: "vdb",
	}, session.devices)
	for i, want := range []string{"nvme1n1", "sda", "sdb", "vda", "vdb"} {
		require.Equal(t, want, string(bytes.TrimRight(object.keys[i], "\x00")))
	}
	require.Equal(t, map[uint64]ioLatencyDiskCounters{
		7: {Major: 259, Minor: 7, Scsi: 1}, 8: {Major: 259, Minor: 8, Scsi: 1},
		9: {Major: 259, Minor: 9}, 10: {Major: 259, Minor: 10},
	}, object.host)
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
