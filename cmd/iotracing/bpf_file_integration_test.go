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

// Qualify file accounting, metadata initialization and bounded-map capacity.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestIOTracingFilemapCapacity exercises the production filemap probe with
// real map exhaustion. It deliberately excludes unrelated programs and does
// not replace full iotracing qualification. Set TEST_IOTRACING_FILE_BPF to the
// object path and TMPDIR to an ext4/XFS directory; the test takes seconds.
// This capacity gate requires an object with io_source_full.
func TestIOTracingFilemapCapacity(t *testing.T) {
	object := os.Getenv("TEST_IOTRACING_FILE_BPF")
	require.NotEmpty(t, object, "run make integration to supply the production BPF object")
	require.Equal(t, 0, os.Geteuid(), "live filemap qualification requires root")
	require.NotEmpty(t, os.Getenv("TMPDIR"), "set TMPDIR to an ext4/XFS test directory")
	require.NoError(t, unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY,
	}))

	dir := t.TempDir()
	var fs unix.Statfs_t
	require.NoError(t, unix.Statfs(dir, &fs))
	require.True(t, fs.Type == unix.EXT4_SUPER_MAGIC || fs.Type == unix.XFS_SUPER_MAGIC,
		"TMPDIR must be on ext4 or XFS")

	type sourceKey struct {
		PID   uint32
		Dev   uint32
		Inode uint64
	}
	type fileMapping struct {
		key  sourceKey
		page []byte
	}
	files := make([]fileMapping, 0, 3)
	for _, name := range []string{"fresh", "seeded", "overflow"} {
		file, err := os.Create(filepath.Join(dir, name))
		require.NoError(t, err)
		t.Cleanup(func() { _ = file.Close() })
		n, err := file.Write(make([]byte, os.Getpagesize()))
		require.NoError(t, err)
		require.Equal(t, os.Getpagesize(), n)
		require.NoError(t, file.Sync())
		var stat unix.Stat_t
		require.NoError(t, unix.Fstat(int(file.Fd()), &stat))
		page, err := unix.Mmap(int(file.Fd()), 0, os.Getpagesize(),
			unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE)
		require.NoError(t, err)
		t.Cleanup(func() { _ = unix.Munmap(page) })
		files = append(files, fileMapping{
			key:  sourceKey{Dev: unix.Major(stat.Dev)<<20 | unix.Minor(stat.Dev), Inode: stat.Ino},
			page: page,
		})
	}
	t.Logf("temporary files: filesystem=%#x device=%d:%d inodes=%d,%d,%d",
		fs.Type, files[0].key.Dev>>20, files[0].key.Dev&((1<<20)-1),
		files[0].key.Inode, files[1].key.Inode, files[2].key.Inode)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	fault := func(file fileMapping) {
		t.Helper()
		require.NoError(t, unix.Madvise(file.page, unix.MADV_DONTNEED))
		// A private write fault bypasses the read-only fault-around shortcut.
		file.page[0] = 1
	}

	// Loading a second object must not inherit the first capture's full flag.
	for session := 0; session < 2; session++ {
		spec, err := ebpf.LoadCollectionSpec(object)
		require.NoError(t, err)
		for name := range spec.Programs {
			if name != "bpf_filemap_fault" {
				delete(spec.Programs, name)
			}
		}
		require.NotNil(t, spec.Maps[bpfSourceMapName])
		require.Equal(t, uint32(512), spec.Maps[bpfSourceMapName].MaxEntries)
		require.NotNil(t, spec.Maps["io_source_full"], "capacity qualification requires io_source_full")
		require.NoError(t, spec.RewriteConstants(map[string]any{
			"FILTER_DEV_COUNT": uint32(1),
			"FILTER_DEV_IDS":   [16]uint32{files[0].key.Dev},
		}))
		coll, err := ebpf.NewCollection(spec)
		require.NoError(t, err)
		t.Cleanup(coll.Close)
		source := coll.Maps[bpfSourceMapName]
		require.NotNil(t, source)
		occupancy := func() int {
			t.Helper()
			var key sourceKey
			var record bpfFilesystemIO
			entries := source.Iterate()
			count := 0
			for entries.Next(&key, &record) {
				count++
			}
			require.NoError(t, entries.Err())
			return count
		}
		require.Zero(t, occupancy(), "capture %d inherited source records", session)
		full := coll.Maps["io_source_full"]
		require.NotNil(t, full)
		var saturated uint32
		require.NoError(t, full.Lookup(uint32(0), &saturated))
		require.Zero(t, saturated)

		// Block completion may install a record without file metadata.
		seed := bpfFilesystemIO{
			DevID: files[1].key.Dev, Ino: files[1].key.Inode,
			BlockReadBytes: 8192, BlockWriteBytes: 16384, BlkcgID: 0x1234,
			Latency: bpfBlockLatency{
				Count: 3, MaxD2CNs: 13, SumD2CNs: 23, MaxQ2CNs: 17, SumQ2CNs: 31,
			},
		}
		require.NoError(t, source.Update(files[1].key, seed, ebpf.UpdateNoExist))
		probe, err := link.Kprobe("filemap_fault", coll.Programs["bpf_filemap_fault"], nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = probe.Close() })
		fault(files[0])
		var fresh bpfFilesystemIO
		require.NoError(t, source.Lookup(files[0].key, &fresh))
		require.Equal(t, files[0].key.Dev, fresh.DevID)
		require.Equal(t, files[0].key.Inode, fresh.Ino)
		require.Equal(t, uint32(os.Getpid()), fresh.TGID)
		require.Equal(t, uint32(1), fresh.PathInitialized)
		require.Equal(t, "fresh", filepath.Base(fresh.PathName()))
		require.Equal(t, uint64(4096), fresh.FsReadBytes)
		require.Zero(t, fresh.FsWriteBytes)
		require.NoError(t, full.Lookup(uint32(0), &saturated))
		require.Zero(t, saturated, "a successful insert must not latch the map full")

		// Fill the actual hash map; the production update helper, not the
		// test, must set the capacity flag when a new file is observed.
		for i := uint64(0); i < 512; i++ {
			err = source.Put(sourceKey{Dev: ^uint32(0), Inode: i}, bpfFilesystemIO{})
			if errors.Is(err, unix.E2BIG) {
				break
			}
			require.NoError(t, err)
		}
		require.ErrorIs(t, err, unix.E2BIG, "the real HASH must reject an additional key")
		require.Equal(t, 512, occupancy())
		require.NoError(t, full.Lookup(uint32(0), &saturated))
		require.Zero(t, saturated, "userspace filling the HASH must not set the production flag")
		fault(files[2])
		require.NoError(t, full.Lookup(uint32(0), &saturated))
		require.Equal(t, uint32(1), saturated)
		var overflow bpfFilesystemIO
		require.ErrorIs(t, source.Lookup(files[2].key, &overflow), ebpf.ErrKeyNotExist)
		fault(files[2])
		require.ErrorIs(t, source.Lookup(files[2].key, &overflow), ebpf.ErrKeyNotExist)
		require.Equal(t, 512, occupancy())

		fault(files[0])
		var hot bpfFilesystemIO
		require.NoError(t, source.Lookup(files[0].key, &hot))
		fresh.FsReadBytes += 4096
		require.Equal(t, fresh, hot, "a full map must still account for admitted files")

		fault(files[1])
		var seeded bpfFilesystemIO
		require.NoError(t, source.Lookup(files[1].key, &seeded))
		require.Equal(t, seed.BlockReadBytes, seeded.BlockReadBytes)
		require.Equal(t, seed.BlockWriteBytes, seeded.BlockWriteBytes)
		require.Equal(t, seed.BlkcgID, seeded.BlkcgID)
		require.Equal(t, seed.Latency, seeded.Latency)
		require.Equal(t, files[1].key.Dev, seeded.DevID)
		require.Equal(t, files[1].key.Inode, seeded.Ino)
		require.Equal(t, uint32(os.Getpid()), seeded.TGID)
		require.Equal(t, uint32(1), seeded.PathInitialized)
		require.Equal(t, "seeded", filepath.Base(seeded.PathName()))
		require.NotEqual(t, [16]byte{}, seeded.Comm)
		require.Equal(t, uint64(4096), seeded.FsReadBytes)
		require.Zero(t, seeded.FsWriteBytes)
		before := seeded
		fault(files[1])
		require.NoError(t, source.Lookup(files[1].key, &seeded))
		before.FsReadBytes += 4096
		require.Equal(t, before, seeded)
		require.Equal(t, 512, occupancy())
		require.NoError(t, full.Lookup(uint32(0), &saturated))
		require.Equal(t, uint32(1), saturated)
		require.NoError(t, probe.Close())
		coll.Close()
	}
}

// TestIOTracingReadWrite requires TEST_IOTRACING_FILE_BPF and an explicit
// ext4/XFS TMPDIR. It isolates the two file-entry programs, not the full trace.
func TestIOTracingReadWrite(t *testing.T) {
	object := os.Getenv("TEST_IOTRACING_FILE_BPF")
	require.NotEmpty(t, object, "run make integration to supply the production BPF object")
	require.Equal(t, 0, os.Geteuid(), "live read/write qualification requires root")
	require.NotEmpty(t, os.Getenv("TMPDIR"), "set TMPDIR to an ext4/XFS test directory")
	require.NoError(t, unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY,
	}))

	dir := t.TempDir()
	var fs unix.Statfs_t
	require.NoError(t, unix.Statfs(dir, &fs))
	require.True(t, fs.Type == unix.EXT4_SUPER_MAGIC || fs.Type == unix.XFS_SUPER_MAGIC,
		"TMPDIR must be on ext4 or XFS")
	filesystem := "ext4"
	if fs.Type == unix.XFS_SUPER_MAGIC {
		filesystem = "xfs"
	}
	file, err := os.Create(filepath.Join(dir, "read-write"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	data := make([]byte, 4096)
	n, err := file.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)
	require.NoError(t, file.Sync())
	fd := int(file.Fd())
	var stat unix.Stat_t
	require.NoError(t, unix.Fstat(fd, &stat))
	t.Logf("temporary file: filesystem=%s device=%d:%d inode=%d",
		filesystem, unix.Major(stat.Dev), unix.Minor(stat.Dev), stat.Ino)

	directFD, err := unix.Open(file.Name(), unix.O_RDWR|unix.O_DIRECT|unix.O_CLOEXEC, 0)
	require.NoError(t, err, "temporary file must support Direct IO")
	t.Cleanup(func() { _ = unix.Close(directFD) })
	aligned, err := unix.Mmap(-1, 0, 4096, unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_PRIVATE|unix.MAP_ANON)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Munmap(aligned)) })

	type sourceKey struct {
		PID   uint32
		Dev   uint32
		Inode uint64
	}
	key := sourceKey{Dev: unix.Major(stat.Dev)<<20 | unix.Minor(stat.Dev), Inode: stat.Ino}
	spec, err := ebpf.LoadCollectionSpec(object)
	require.NoError(t, err)
	for name := range spec.Programs {
		if name != "bpf_anyfs_file_read_iter" && name != "bpf_anyfs_file_write_iter" {
			delete(spec.Programs, name)
		}
	}
	require.Len(t, spec.Programs, 2)
	require.NoError(t, spec.RewriteConstants(map[string]any{
		"FILTER_DEV_COUNT": uint32(1),
		"FILTER_DEV_IDS":   [16]uint32{key.Dev},
	}))
	coll, err := ebpf.NewCollection(spec)
	require.NoError(t, err, "production read/write programs must relocate and load")
	t.Cleanup(coll.Close)
	source := coll.Maps[bpfSourceMapName]
	require.NotNil(t, source)
	readProbe, err := link.Kprobe(filesystem+"_file_read_iter",
		coll.Programs["bpf_anyfs_file_read_iter"], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = readProbe.Close() })
	writeProbe, err := link.Kprobe(filesystem+"_file_write_iter",
		coll.Programs["bpf_anyfs_file_write_iter"], nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writeProbe.Close() })

	// A block completion can publish a row before any file event. Leave its
	// file metadata empty and verify real entries fill it without losing IO.
	seed := bpfFilesystemIO{
		TGID:            uint32(os.Getpid()),
		DevID:           key.Dev,
		Ino:             key.Inode,
		BlockReadBytes:  8192,
		BlockWriteBytes: 16384,
		Latency: bpfBlockLatency{
			Count: 3, MaxD2CNs: 13, SumD2CNs: 23, MaxQ2CNs: 17, SumQ2CNs: 31,
		},
	}
	require.NoError(t, source.Update(key, seed, ebpf.UpdateNoExist))
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var wantRead, wantWrite uint64
	check := func(step string, read, write uint64) bpfFilesystemIO {
		t.Helper()
		wantRead += read
		wantWrite += write
		var record bpfFilesystemIO
		require.NoError(t, source.Lookup(key, &record), step)
		require.Equal(t, wantRead, record.FsReadBytes, "%s: requested read bytes", step)
		require.Equal(t, wantWrite, record.FsWriteBytes, "%s: requested write bytes", step)
		require.Equal(t, key.Dev, record.DevID, step)
		require.Equal(t, key.Inode, record.Ino, step)
		require.Equal(t, uint32(os.Getpid()), record.TGID, step)
		require.Equal(t, uint32(1), record.PathInitialized, step)
		require.Equal(t, filepath.Base(file.Name()), filepath.Base(record.PathName()), step)
		require.NotEqual(t, [16]byte{}, record.Comm, step)
		require.Equal(t, seed.BlockReadBytes, record.BlockReadBytes, step)
		require.Equal(t, seed.BlockWriteBytes, record.BlockWriteBytes, step)
		require.Equal(t, seed.Latency, record.Latency, step)
		return record
	}

	// In 4.18/5.10 fs/read_write.c, new_sync_read/write passes len as
	// iov_iter.count to these hooks. Use one syscall per step, including
	// short/EOF reads and Direct IO rejected later by alignment checks.
	n, err = unix.Pread(fd, data[:513], 0)
	require.NoError(t, err)
	require.Equal(t, 513, n)
	normalRead := check("read", 513, 0)
	n, err = unix.Pwrite(fd, data[:1027], 0)
	require.NoError(t, err)
	require.Equal(t, 1027, n)
	normalWrite := check("write", 0, 1027)

	n, err = unix.Pread(fd, data[:2048], 3072)
	require.NoError(t, err)
	require.Equal(t, 1024, n)
	require.Equal(t, normalRead.Flags, check("short read", 2048, 0).Flags)
	n, err = unix.Pread(fd, data[:256], 4096)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, normalRead.Flags, check("EOF read", 256, 0).Flags)

	// IOCB_DIRECT's bit position differs across target kernel backports.
	// Check raw flags change for Direct IO and return to the ordinary value.
	n, err = unix.Pread(directFD, aligned, 0)
	require.NoError(t, err)
	require.Equal(t, len(aligned), n)
	directRead := check("Direct read", 4096, 0)
	require.NotEqual(t, normalRead.Flags, directRead.Flags)
	n, err = unix.Pread(fd, data[:73], 0)
	require.NoError(t, err)
	require.Equal(t, 73, n)
	require.Equal(t, normalRead.Flags, check("ordinary read after Direct", 73, 0).Flags)

	n, err = unix.Pwrite(directFD, aligned, 0)
	require.NoError(t, err)
	require.Equal(t, len(aligned), n)
	directWrite := check("Direct write", 0, 4096)
	require.NotEqual(t, normalWrite.Flags, directWrite.Flags)
	n, err = unix.Pwrite(fd, data[:87], 0)
	require.NoError(t, err)
	require.Equal(t, 87, n)
	require.Equal(t, normalWrite.Flags, check("ordinary write after Direct", 0, 87).Flags)

	_, err = unix.Pread(directFD, aligned, 1)
	require.ErrorIs(t, err, unix.EINVAL, "non-aligned Direct read must reach the failing entry")
	require.Equal(t, directRead.Flags, check("failed Direct read", 4096, 0).Flags)
	_, err = unix.Pwrite(directFD, aligned, 1)
	require.ErrorIs(t, err, unix.EINVAL, "non-aligned Direct write must reach the failing entry")
	last := check("failed Direct write", 0, 4096)
	require.Equal(t, directWrite.Flags, last.Flags)
	require.Equal(t, normalRead.Comm, last.Comm)
	require.Equal(t, normalRead.PathSegs, last.PathSegs)
	t.Logf("raw flags: ordinary read=%#x write=%#x; Direct read=%#x write=%#x",
		normalRead.Flags, normalWrite.Flags, directRead.Flags, directWrite.Flags)
}

// TestIOTracingFileMetadata requires TEST_IOTRACING_FILE_BPF and an explicit
// ext4/XFS TMPDIR. Capacity qualification is separate.
func TestIOTracingFileMetadata(t *testing.T) {
	object := os.Getenv("TEST_IOTRACING_FILE_BPF")
	require.NotEmpty(t, object, "run make integration to supply the production BPF object")
	require.Equal(t, 0, os.Geteuid(), "live file metadata qualification requires root")
	require.NotEmpty(t, os.Getenv("TMPDIR"), "set TMPDIR to an ext4/XFS test directory")
	require.NoError(t, unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY,
	}))
	dir := t.TempDir()
	var fs unix.Statfs_t
	require.NoError(t, unix.Statfs(dir, &fs))
	require.True(t, fs.Type == unix.EXT4_SUPER_MAGIC || fs.Type == unix.XFS_SUPER_MAGIC,
		"TMPDIR must be on ext4 or XFS")
	filesystem, mkwrite := "ext4", "ext4_page_mkwrite"
	if fs.Type == unix.XFS_SUPER_MAGIC {
		filesystem, mkwrite = "xfs", "xfs_filemap_page_mkwrite"
	}

	type sourceKey struct {
		PID   uint32
		Dev   uint32
		Inode uint64
	}
	for _, tc := range []struct {
		name, program, symbol string
		mapping               int
		read, write           uint64
	}{
		{name: "read", program: "bpf_anyfs_file_read_iter", symbol: filesystem + "_file_read_iter", read: 17},
		{name: "write", program: "bpf_anyfs_file_write_iter", symbol: filesystem + "_file_write_iter", write: 29},
		{name: "private_fault", program: "bpf_filemap_fault", symbol: "filemap_fault", mapping: unix.MAP_PRIVATE, read: 4096},
		{name: "shared_mkwrite", program: "bpf_anyfs_filemap_page_mkwrite", symbol: mkwrite, mapping: unix.MAP_SHARED, write: 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type testFile struct {
				file *os.File
				key  sourceKey
				page []byte
				seed bpfFilesystemIO
			}
			var files []testFile
			data := make([]byte, os.Getpagesize())
			for i, name := range []string{"fresh-metadata-name-longer-than-inline-limit", "seeded"} {
				file, err := os.Create(filepath.Join(t.TempDir(), name))
				require.NoError(t, err)
				t.Cleanup(func() { _ = file.Close() })
				n, err := file.Write(data)
				require.NoError(t, err)
				require.Equal(t, len(data), n)
				require.NoError(t, file.Sync())
				var stat unix.Stat_t
				require.NoError(t, unix.Fstat(int(file.Fd()), &stat))
				entry := testFile{
					file: file,
					key: sourceKey{
						Dev: unix.Major(stat.Dev)<<20 | unix.Minor(stat.Dev), Inode: stat.Ino,
					},
				}
				if tc.mapping != 0 {
					page, err := unix.Mmap(int(file.Fd()), 0, len(data),
						unix.PROT_READ|unix.PROT_WRITE, tc.mapping)
					require.NoError(t, err)
					t.Cleanup(func() { require.NoError(t, unix.Munmap(page)) })
					entry.page = page
				}
				if i == 1 {
					// This opaque cgroup value is retained, not dereferenced by
					// file probes. The path flag stays clear for metadata completion.
					entry.seed = bpfFilesystemIO{
						DevID: entry.key.Dev, Ino: entry.key.Inode,
						BlockReadBytes: 8192, BlockWriteBytes: 16384, BlkcgID: 0x1234,
						Latency: bpfBlockLatency{
							Count: 3, MaxD2CNs: 13, SumD2CNs: 23, MaxQ2CNs: 17, SumQ2CNs: 31,
						},
					}
				}
				files = append(files, entry)
				t.Logf("%s: filesystem=%s device=%d:%d inode=%d",
					name, filesystem, unix.Major(stat.Dev), unix.Minor(stat.Dev), stat.Ino)
			}

			spec, err := ebpf.LoadCollectionSpec(object)
			require.NoError(t, err)
			for name := range spec.Programs {
				if name != tc.program {
					delete(spec.Programs, name)
				}
			}
			require.Len(t, spec.Programs, 1)
			require.NoError(t, spec.RewriteConstants(map[string]any{
				"FILTER_DEV_COUNT": uint32(1),
				"FILTER_DEV_IDS":   [16]uint32{files[0].key.Dev},
			}))
			coll, err := ebpf.NewCollection(spec)
			require.NoError(t, err, "production file program must relocate and load")
			t.Cleanup(coll.Close)
			source := coll.Maps[bpfSourceMapName]
			require.NotNil(t, source)
			var absent bpfFilesystemIO
			require.ErrorIs(t, source.Lookup(files[0].key, &absent), ebpf.ErrKeyNotExist)
			require.NoError(t, source.Update(files[1].key, files[1].seed, ebpf.UpdateNoExist))
			probe, err := link.Kprobe(tc.symbol, coll.Programs[tc.program], nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = probe.Close() })
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()

			for _, file := range files {
				var previous bpfFilesystemIO
				for hit := uint64(1); hit <= 2; hit++ {
					switch tc.name {
					case "read":
						n, err := unix.Pread(int(file.file.Fd()), data[:17], 0)
						require.NoError(t, err)
						require.Equal(t, 17, n)
					case "write":
						n, err := unix.Pwrite(int(file.file.Fd()), data[:29], 0)
						require.NoError(t, err)
						require.Equal(t, 29, n)
					default:
						// Both target kernels' do_cow_fault call the file fault
						// directly; private writes avoid read fault-around.
						// Shared writes instead exercise page_mkwrite. Dropping
						// this mapping's PTEs makes each store enter its hook.
						require.NoError(t, unix.Madvise(file.page, unix.MADV_DONTNEED))
						file.page[0] = byte(hit)
					}
					var record bpfFilesystemIO
					require.NoError(t, source.Lookup(file.key, &record))
					require.Equal(t, tc.read*hit, record.FsReadBytes)
					require.Equal(t, tc.write*hit, record.FsWriteBytes)
					require.Equal(t, file.key.Dev, record.DevID)
					require.Equal(t, file.key.Inode, record.Ino)
					require.Equal(t, uint32(os.Getpid()), record.TGID)
					require.Equal(t, uint32(1), record.PathInitialized)
					name := filepath.Base(file.file.Name())
					if len(name) > 31 {
						name = name[:28] + "..."
					}
					require.Equal(t, name, filepath.Base(record.PathName()))
					require.NotEqual(t, [16]byte{}, record.Comm)
					require.Equal(t, file.seed.BlockReadBytes, record.BlockReadBytes)
					require.Equal(t, file.seed.BlockWriteBytes, record.BlockWriteBytes)
					require.Equal(t, file.seed.BlkcgID, record.BlkcgID)
					require.Equal(t, file.seed.Latency, record.Latency)
					if hit == 2 {
						previous.FsReadBytes += tc.read
						previous.FsWriteBytes += tc.write
						require.Equal(t, previous, record, "hot hits must retain metadata and flags")
					}
					previous = record
					if full := coll.Maps["io_source_full"]; full != nil {
						var saturated uint32
						require.NoError(t, full.Lookup(uint32(0), &saturated))
						require.Zero(t, saturated, "successful file accounting must not latch full")
					}
				}
			}
		})
	}
}
