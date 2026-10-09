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
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/btf"
	"github.com/cilium/ebpf/link"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/bpf"
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

	t.Run("block_device_with_legacy_type", func(t *testing.T) {
		kernel, err := btf.LoadKernelSpec()
		require.NoError(t, err)
		target := kernel.Copy()
		var request, device, legacy *btf.Struct
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
		if err := target.TypeByName("hd_struct", &legacy); err != nil {
			t.Skip("the running kernel does not retain the legacy partition type")
		}
		for i := range request.Members {
			if request.Members[i].Name == "part" {
				request.Members[i].Type = &btf.Pointer{Target: device}
			}
		}
		blockDevice, err := requestPartIsBlockDevice(target)
		require.NoError(t, err)
		require.True(t, blockDevice)
		spec, err := ebpf.LoadCollectionSpec(object)
		require.NoError(t, err)
		for name := range spec.Programs {
			if name != "bpf_rq_qos_issue" && name != "bpf_rq_qos_done" {
				delete(spec.Programs, name)
			}
		}
		require.NoError(t, spec.RewriteConstants(map[string]any{bpfRequestPartBlockDevice: blockDevice}))
		// Qualify CO-RE and both block programs without attaching a fake layout.
		collection, err := ebpf.NewCollectionWithOptions(spec, ebpf.CollectionOptions{
			Programs: ebpf.ProgramOptions{KernelTypes: target},
		})
		require.NoError(t, err)
		t.Cleanup(collection.Close)
	})

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
		kernel, loadErr := btf.LoadKernelSpec()
		require.NoError(t, loadErr)
		blockDevice, loadErr := requestPartIsBlockDevice(kernel)
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
	kernel, err := btf.LoadKernelSpec()
	require.NoError(t, err)
	blockDevice, err := requestPartIsBlockDevice(kernel)
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
	reader, err := attachAndEventPipe(ctx, program)
	require.NoError(t, err, "all production attach options must succeed")
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	require.NoError(t, program.Detach())
}
