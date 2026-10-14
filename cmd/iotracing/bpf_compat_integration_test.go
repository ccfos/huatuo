//go:build integration && linux

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

// Qualify device attribution and complete production BPF loading and attach.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/cilium/ebpf/link"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/procfs"
)

// TestIOTracingBlockDevice observes real file IO on an ext4/XFS TMPDIR.
func TestIOTracingBlockDevice(t *testing.T) {
	object := os.Getenv("TEST_IOTRACING_FILE_BPF")
	require.NotEmpty(t, object, "run make integration to supply the production BPF object")
	require.Equal(t, 0, os.Geteuid(), "live block-device qualification requires root")
	require.NoError(t, unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY,
	}))

	dir := t.TempDir()
	var fs unix.Statfs_t
	require.NoError(t, unix.Statfs(dir, &fs))
	require.True(t, fs.Type == unix.EXT4_SUPER_MAGIC || fs.Type == unix.XFS_SUPER_MAGIC,
		"TMPDIR must be on ext4 or XFS")
	file, err := os.Create(filepath.Join(dir, "block-device"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	data := make([]byte, 4096)
	n, err := file.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)
	require.NoError(t, file.Sync())
	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(int(file.Fd()), &stat))
	t.Logf("temporary file: filesystem=%#x device=%d:%d inode=%d",
		fs.Type, unix.Major(stat.Dev), unix.Minor(stat.Dev), stat.Ino)

	type sourceKey struct {
		PID   uint32
		Dev   uint32
		Inode uint64
	}
	type requestKey struct {
		Dev    uint32
		Pad    uint32
		Sector uint64
	}
	type requestInfo struct {
		Inode   uint64
		PID     uint32
		Dev     uint32
		Bytes   uint64
		BlkcgID uint64
		Comm    [16]byte
	}
	fileKey := sourceKey{Dev: unix.Major(stat.Dev)<<20 | unix.Minor(stat.Dev), Inode: stat.Ino}

	for _, legacyType := range []bool{true, false} {
		name := "block_device_with_legacy_type"
		if !legacyType {
			name = "block_device_without_legacy_type"
		}
		t.Run(name, func(t *testing.T) {
			kernel, err := btf.LoadKernelSpec()
			require.NoError(t, err)
			target := kernel.Copy()
			var request, device *btf.Struct
			require.NoError(t, target.TypeByName("request", &request))
			devices, err := target.AnyTypesByName("block_device")
			require.NoError(t, err)
			for _, typ := range devices {
				if candidate, ok := btf.UnderlyingType(typ).(*btf.Struct); ok && requestLayoutMember(candidate, "bd_dev") != nil {
					device = candidate
					break
				}
			}
			require.NotNil(t, device, "block_device with bd_dev must exist")
			if legacyType {
				var legacy *btf.Struct
				if err := target.TypeByName("hd_struct", &legacy); err != nil {
					t.Skip("the running kernel does not retain the legacy partition type")
				}
			}
			for i := range request.Members {
				if request.Members[i].Name == "part" {
					request.Members[i].Type = &btf.Pointer{Target: device}
				}
			}
			if !legacyType {
				// Keep references valid without exposing the legacy name to CO-RE.
				// Reparse the BTF to rebuild its type-name index after renaming.
				var types []btf.Type
				iter := target.Iterate()
				for iter.Next() {
					switch typ := iter.Type.(type) {
					case *btf.Struct:
						if typ.Name == "hd_struct" {
							typ.Name = "unavailable_partition"
						}
					case *btf.Fwd:
						if typ.Name == "hd_struct" {
							typ.Name = "unavailable_partition"
						}
					}
					types = append(types, iter.Type)
				}
				builder, err := btf.NewBuilder(types)
				require.NoError(t, err)
				data, err := builder.Marshal(nil, nil)
				require.NoError(t, err)
				target, err = btf.LoadSpecFromReader(bytes.NewReader(data))
				require.NoError(t, err)
				_, err = target.AnyTypesByName("hd_struct")
				require.ErrorIs(t, err, btf.ErrNotFound)
			}
			blockDevice, err := requestPartIsBlockDeviceFromSpec(target)
			require.NoError(t, err)
			require.True(t, blockDevice)
			spec, err := ebpf.LoadCollectionSpec(object)
			require.NoError(t, err)
			for name := range spec.Programs {
				if name != "bpf_rq_qos_issue" && name != "bpf_rq_qos_done" {
					delete(spec.Programs, name)
				}
			}
			require.Len(t, spec.Programs, 2)
			require.NoError(t, spec.RewriteConstants(map[string]any{bpfRequestPartBlockDevice: blockDevice}))
			// Qualify CO-RE and both block programs without attaching a fake layout.
			collection, err := ebpf.NewCollectionWithOptions(spec, ebpf.CollectionOptions{
				Programs: ebpf.ProgramOptions{KernelTypes: target},
			})
			require.NoError(t, err)
			t.Cleanup(collection.Close)
		})
	}

	load := func(t *testing.T, objectPath string, devices []uint32) *ebpf.Collection {
		t.Helper()
		spec, loadErr := ebpf.LoadCollectionSpec(objectPath)
		require.NoError(t, loadErr)
		// Isolate block programs; full-object loading is qualified separately.
		for name := range spec.Programs {
			if name != "bpf_rq_qos_issue" && name != "bpf_rq_qos_done" {
				delete(spec.Programs, name)
			}
		}
		require.Len(t, spec.Programs, 2)
		var ids [16]uint32
		copy(ids[:], devices)
		blockDevice, loadErr := requestPartIsBlockDevice()
		require.NoError(t, loadErr)
		require.NoError(t, spec.RewriteConstants(map[string]any{
			"FILTER_DEV_COUNT":        uint32(len(devices)),
			"FILTER_DEV_IDS":          ids,
			bpfRequestPartBlockDevice: blockDevice,
		}))
		coll, loadErr := ebpf.NewCollection(spec)
		require.NoError(t, loadErr, "production block programs must relocate and load")
		t.Cleanup(coll.Close)
		require.NotNil(t, coll.Maps[bpfSourceMapName])
		require.NotNil(t, coll.Maps["start_info_map"])

		issue, done := "__rq_qos_issue", "__rq_qos_done"
		if bpf.HasKprobeFunction("rq_qos_issue") {
			issue, done = "rq_qos_issue", "rq_qos_done"
		}
		// Install completion first and retire issue first during Cleanup.
		doneProbe, attachErr := link.Kprobe(done, coll.Programs["bpf_rq_qos_done"], nil)
		require.NoError(t, attachErr)
		t.Cleanup(func() { _ = doneProbe.Close() })
		issueProbe, attachErr := link.Kprobe(issue, coll.Programs["bpf_rq_qos_issue"], nil)
		require.NoError(t, attachErr)
		t.Cleanup(func() { _ = issueProbe.Close() })
		return coll
	}
	waitForRecord := func(t *testing.T, coll *ebpf.Collection, key sourceKey) bpfFilesystemIO {
		t.Helper()
		defer func() {
			if !t.Failed() {
				return
			}
			var source sourceKey
			var value bpfFilesystemIO
			entries := coll.Maps[bpfSourceMapName].Iterate()
			for entries.Next(&source, &value) {
				t.Logf("source: key=%+v pid=%d dev=%d inode=%d read=%d write=%d",
					source, value.TGID, value.DevID, value.Ino, value.BlockReadBytes, value.BlockWriteBytes)
			}
			var request requestKey
			var info requestInfo
			pending := coll.Maps["start_info_map"].Iterate()
			for pending.Next(&request, &info) {
				t.Logf("pending: key=%+v info=%+v", request, info)
			}
		}()
		var record bpfFilesystemIO
		require.Eventually(t, func() bool {
			if lookupErr := coll.Maps[bpfSourceMapName].Lookup(key, &record); lookupErr != nil {
				return false
			}
			var request requestKey
			var info requestInfo
			pending := coll.Maps["start_info_map"].Iterate()
			for pending.Next(&request, &info) {
				if info.Dev == key.Dev && info.Inode == key.Inode {
					return false
				}
			}
			return pending.Err() == nil
		}, 3*time.Second, 10*time.Millisecond,
			"expected source record and drained issue state for %+v", key)
		require.Equal(t, key.Dev, record.DevID)
		require.Equal(t, key.Inode, record.Ino)
		require.NotZero(t, record.Latency.Count, "block completion must consume a matching issue")
		require.Zero(t, record.FsReadBytes)
		require.Zero(t, record.FsWriteBytes)
		return record
	}

	for _, tc := range []struct {
		name    string
		devices []uint32
	}{
		{name: "file_unfiltered"},
		{name: "file_device_filter", devices: []uint32{fileKey.Dev}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coll := load(t, object, tc.devices)
			data[0]++
			written, writeErr := file.WriteAt(data, 0)
			require.NoError(t, writeErr)
			require.Equal(t, len(data), written)
			require.NoError(t, file.Sync())
			record := waitForRecord(t, coll, fileKey)
			require.GreaterOrEqual(t, record.BlockWriteBytes, uint64(len(data)))
			require.Zero(t, record.BlockReadBytes)
		})
	}

	t.Run("file_excluded", func(t *testing.T) {
		// This invalid device cannot accept real IO from the temporary file.
		coll := load(t, object, []uint32{^uint32(0)})
		data[0]++
		written, writeErr := file.WriteAt(data, 0)
		require.NoError(t, writeErr)
		require.Equal(t, len(data), written)
		require.NoError(t, file.Sync())
		// Completion accounting can follow the wakeup observed by fsync.
		time.Sleep(50 * time.Millisecond)
		var key sourceKey
		var record bpfFilesystemIO
		source := coll.Maps[bpfSourceMapName].Iterate()
		require.False(t, source.Next(&key, &record), "excluded IO created a source record")
		require.NoError(t, source.Err())
		var request requestKey
		var info requestInfo
		pending := coll.Maps["start_info_map"].Iterate()
		require.False(t, pending.Next(&request, &info), "excluded IO left issue state")
		require.NoError(t, pending.Err())
	})
}

// TestIOTracingFullObject checks real verifier and production attach behavior;
// it does not qualify capture results or the automatic triggering workflow.
func TestIOTracingFullObject(t *testing.T) {
	object := os.Getenv("TEST_IOTRACING_FILE_BPF")
	require.NotEmpty(t, object, "run make integration to supply the production BPF object")
	require.Equal(t, 0, os.Geteuid(), "live full-object qualification requires root")
	require.NotEmpty(t, os.Getenv("TMPDIR"), "set TMPDIR to an ext4/XFS test directory")
	require.NoError(t, unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY,
	}))
	var fs unix.Statfs_t
	require.NoError(t, unix.Statfs(t.TempDir(), &fs))
	require.True(t, fs.Type == unix.EXT4_SUPER_MAGIC || fs.Type == unix.XFS_SUPER_MAGIC,
		"TMPDIR must be on ext4 or XFS")
	contents, err := os.ReadFile(object)
	require.NoError(t, err)
	// LoadBPFFromBytes passes the complete production collection to the
	// verifier. No programs or relocations are removed for this gate.
	blockDevice, err := requestPartIsBlockDevice()
	require.NoError(t, err)
	program, err := bpf.LoadBPFFromBytes("iotracing", contents, map[string]any{
		bpfRequestPartBlockDevice: blockDevice,
	})
	require.NoError(t, err, "complete production object must relocate and load")
	t.Cleanup(func() { require.NoError(t, program.Close()) })
	for _, name := range []string{
		"bpf_rq_qos_issue", "bpf_rq_qos_done",
		"bpf_anyfs_file_read_iter", "bpf_anyfs_file_write_iter",
		"bpf_anyfs_filemap_page_mkwrite", "bpf_filemap_fault",
		"bpf_io_schedule", "bpf_return_io_schedule",
		"bpf_io_schedule_timeout", "bpf_return_io_schedule_timeout",
	} {
		require.NotZero(t, program.ProgramIDByName(name), "missing production program %s", name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reader, _, err := attachAndEventPipe(ctx, &ioTracingProbeQualifier{BPF: program})
	if errors.Is(err, errXFSKprobeUnsupported) {
		t.Skipf("XFS qualification unavailable: %v", err)
	}
	require.NoError(t, err, "all production attach options must succeed")
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	require.NoError(t, program.Detach())
}

// Keep the production attach order while identifying the rejected probe.
// Only qualification classifies unavailable XFS; production retains the error.
type ioTracingProbeQualifier struct {
	bpf.BPF
}

func (b *ioTracingProbeQualifier) AttachWithOptions(options []bpf.AttachOption) error {
	for _, option := range options {
		if err := b.BPF.AttachWithOptions([]bpf.AttachOption{option}); err != nil {
			return xfsKprobeFailure(option.Symbol, err)
		}
	}
	return nil
}

var errXFSKprobeUnsupported = errors.New("XFS module name-based kprobes are unsupported")

func isXFSFileProbe(name string) bool {
	switch name {
	case "xfs_file_read_iter", "xfs_file_write_iter", "xfs_filemap_page_mkwrite":
		return true
	default:
		return false
	}
}

// A kernel may count one module symbol twice when registering a named kprobe.
// Skip only with unique, visible XFS symbols; real ambiguity must remain a failure.
func xfsKprobeFailure(name string, attachErr error) error {
	if !isXFSFileProbe(name) || !errors.Is(attachErr, syscall.EADDRNOTAVAIL) {
		return attachErr
	}
	path := procfs.Path("kallsyms")
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("inspect XFS symbols after (%w): %w", attachErr, err)
	}
	defer file.Close()

	matches := map[string][]string{
		"xfs_file_read_iter": nil, "xfs_file_write_iter": nil, "xfs_filemap_page_mkwrite": nil,
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 3 && isXFSFileProbe(fields[2]) {
			// Count every exact name, including other modules and equal addresses.
			matches[fields[2]] = append(matches[fields[2]], scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read XFS symbols after (%w): %w", attachErr, err)
	}
	for symbol, entries := range matches {
		if len(entries) == 0 {
			return fmt.Errorf("missing symbol %q in %s after (%w)", symbol, path, attachErr)
		}
		if len(entries) != 1 {
			return fmt.Errorf("multiple entries for %q in %s after (%w)", symbol, path, attachErr)
		}
		fields := strings.Fields(entries[0])
		if len(fields) != 3 && len(fields) != 4 {
			return fmt.Errorf("malformed symbol %q in %s after (%w)", symbol, path, attachErr)
		}
		if fields[1] != "t" && fields[1] != "T" {
			return fmt.Errorf("symbol %q is not text in %s after (%w)", symbol, path, attachErr)
		}
		address, err := strconv.ParseUint(fields[0], 16, 64)
		if err != nil {
			return fmt.Errorf("parse %q address after (%w): %w", symbol, attachErr, err)
		}
		if address == 0 {
			return fmt.Errorf("hidden address for %q in %s after (%w)", symbol, path, attachErr)
		}
		if len(fields) != 4 || fields[3] != "[xfs]" {
			return fmt.Errorf("symbol %q is not in the XFS module after (%w)", symbol, attachErr)
		}
	}
	// This proves name-based attachment is unavailable, not the kernel bug's cause.
	return fmt.Errorf("%w: %s: %w", errXFSKprobeUnsupported, name, attachErr)
}

func TestIOTracingXFSSymbolQualification(t *testing.T) {
	for _, test := range []struct {
		name, readLine, detail string
		otherSymbol            string
		missingFile            bool
		cause                  error
	}{
		{name: "missing_file", missingFile: true, cause: os.ErrNotExist},
		{name: "missing_target", detail: "xfs_file_read_iter"},
		{name: "hidden_target", readLine: "0000000000000000 T xfs_file_read_iter [xfs]\n", detail: "hidden"},
		{
			name: "two_addresses", detail: "multiple",
			readLine: "ffffffffc0001000 T xfs_file_read_iter [xfs]\n" +
				"ffffffffc0001001 t xfs_file_read_iter [xfs]\n",
		},
		{
			name: "same_address", detail: "multiple",
			readLine: "ffffffffc0001000 T xfs_file_read_iter [xfs]\n" +
				"ffffffffc0001000 t xfs_file_read_iter [xfs]\n",
		},
		{
			name: "other_module_duplicate", detail: "multiple",
			readLine: "ffffffffc0001000 T xfs_file_read_iter [xfs]\n" +
				"ffffffffc0009000 t xfs_file_read_iter [other]\n",
		},
		{
			name: "non_text_duplicate", detail: "multiple",
			readLine: "ffffffffc0001000 T xfs_file_read_iter [xfs]\n" +
				"ffffffffc0009000 D xfs_file_read_iter\n",
		},
		{name: "invalid_hex", readLine: "nothex T xfs_file_read_iter [xfs]\n", cause: strconv.ErrSyntax},
		{name: "address_overflow", readLine: "10000000000000000 T xfs_file_read_iter [xfs]\n", cause: strconv.ErrRange},
		{name: "non_text_target", readLine: "ffffffffc0001000 D xfs_file_read_iter [xfs]\n", detail: "text"},
		{name: "builtin_target", readLine: "ffffffffc0001000 T xfs_file_read_iter\n", detail: "module"},
		{name: "other_module_target", readLine: "ffffffffc0001000 T xfs_file_read_iter [other]\n", detail: "module"},
		{name: "malformed_target", readLine: "ffffffffc0001000 T xfs_file_read_iter [xfs] extra\n", detail: "malformed"},
		{
			name: "write_in_other_module", readLine: "ffffffffc0001000 T xfs_file_read_iter [xfs]\n",
			otherSymbol: "xfs_file_write_iter", detail: "module",
		},
		{
			name: "mkwrite_in_other_module", readLine: "ffffffffc0001000 T xfs_file_read_iter [xfs]\n",
			otherSymbol: "xfs_filemap_page_mkwrite", detail: "module",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			symbols := test.readLine + "ffffffffc0002000 T xfs_file_write_iter [xfs]\n" +
				"ffffffffc0003000 T xfs_filemap_page_mkwrite [xfs]\n"
			if test.otherSymbol != "" {
				symbols = strings.Replace(symbols, test.otherSymbol+" [xfs]", test.otherSymbol+" [other]", 1)
			}
			installAttachKallsyms(t, symbols, test.missingFile)
			firstErr := fmt.Errorf("attach failed: %w", syscall.EADDRNOTAVAIL)
			b := &attachTestBPF{reader: new(attachTestReader), failures: map[string][]error{"xfs_file_read_iter": {firstErr}}}
			err := (&ioTracingProbeQualifier{BPF: b}).AttachWithOptions([]bpf.AttachOption{{Symbol: "xfs_file_read_iter"}})
			require.ErrorIs(t, err, firstErr)
			require.NotErrorIs(t, err, errXFSKprobeUnsupported)
			if test.cause != nil {
				require.ErrorIs(t, err, test.cause)
			}
			if test.detail != "" {
				require.ErrorContains(t, err, test.detail)
			}
		})
	}
	t.Run("missing_later_symbol", func(t *testing.T) {
		installAttachKallsyms(t, "ffffffffc0001000 T xfs_file_read_iter [xfs]\n", false)
		b := &attachTestBPF{
			reader:   new(attachTestReader),
			failures: map[string][]error{"xfs_file_read_iter": {syscall.EADDRNOTAVAIL}},
		}
		err := (&ioTracingProbeQualifier{BPF: b}).AttachWithOptions([]bpf.AttachOption{{Symbol: "xfs_file_read_iter"}})
		require.ErrorContains(t, err, "missing")
		require.NotErrorIs(t, err, errXFSKprobeUnsupported)
	})
}

func TestIOTracingXFSQualificationScope(t *testing.T) {
	for _, test := range []struct {
		name, symbol string
		cause        error
		wantSkip     bool
	}{
		{name: "xfs_read", symbol: "xfs_file_read_iter", cause: syscall.EADDRNOTAVAIL, wantSkip: true},
		{name: "xfs_write", symbol: "xfs_file_write_iter", cause: syscall.EADDRNOTAVAIL, wantSkip: true},
		{name: "xfs_mkwrite", symbol: "xfs_filemap_page_mkwrite", cause: syscall.EADDRNOTAVAIL, wantSkip: true},
		{name: "non_xfs", symbol: "vfs_read", cause: syscall.EADDRNOTAVAIL},
		{name: "xfs_permission", symbol: "xfs_file_read_iter", cause: syscall.EPERM},
		{name: "xfs_missing", symbol: "xfs_file_read_iter", cause: syscall.ENOENT},
	} {
		t.Run(test.name, func(t *testing.T) {
			installAttachKallsyms(t, attachXFSKallsyms(), false)
			failure := fmt.Errorf("attach %s: %w", test.symbol, test.cause)
			b := &attachTestBPF{failures: map[string][]error{test.symbol: {failure}}}
			err := (&ioTracingProbeQualifier{BPF: b}).AttachWithOptions([]bpf.AttachOption{{Symbol: test.symbol}})
			require.ErrorIs(t, err, failure)
			if test.wantSkip {
				require.ErrorIs(t, err, errXFSKprobeUnsupported)
			} else {
				require.NotErrorIs(t, err, errXFSKprobeUnsupported)
			}
		})
	}
}

func attachXFSKallsyms() string {
	return "ffffffffc0001000 T xfs_file_read_iter [xfs]\n" +
		"ffffffffc0002000 t xfs_file_write_iter [xfs]\n" +
		"ffffffffc0003000 T xfs_filemap_page_mkwrite [xfs]\n"
}

func installAttachKallsyms(t *testing.T, symbols string, missing bool) {
	t.Helper()
	root := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() { procfs.RootPrefix(root) })
	require.NoError(t, os.MkdirAll(procfs.DefaultPath(), 0o755))
	if !missing {
		require.NoError(t, os.WriteFile(procfs.Path("kallsyms"), []byte(symbols), 0o600))
	}
}
