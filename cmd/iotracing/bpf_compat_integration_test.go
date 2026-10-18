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
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
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

// TestIOTracingKernel owns its filesystems so the Go qualification can run
// independently of the public CLI integration cases.
func TestIOTracingKernel(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("set TEST_INTEGRATION=true for live iotracing qualification")
	}
	if os.Geteuid() != 0 {
		t.Skip("live iotracing qualification requires root")
	}
	compiler := strings.Fields(os.Getenv("CC"))
	if len(compiler) == 0 {
		compiler = []string{"cc"}
	}
	for _, name := range append([]string{"losetup", "mount", "umount"}, compiler[0]) {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("iotracing qualification requires %s: %v", name, err)
		}
	}
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); os.IsNotExist(err) {
		t.Skip("kernel BTF is unavailable")
	} else {
		require.NoError(t, err)
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	t.Cleanup(stop)
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		t.Cleanup(cancel)
	}
	object := os.Getenv("TEST_IOTRACING_FILE_BPF")
	if object == "" {
		var err error
		object, err = filepath.Abs(filepath.Join("..", "..", "bpf", "iotracing.o"))
		require.NoError(t, err)
	}
	require.FileExists(t, object, "run make build before kernel qualification")
	t.Setenv("TEST_IOTRACING_FILE_BPF", object)
	work, err := os.MkdirTemp("/var/tmp", "huatuo-iotracing-kernel-")
	require.NoError(t, err)
	t.Logf("iotracing owned resources: %s", work)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("retaining iotracing evidence: %s", work)
			return
		}
		require.NoError(t, os.RemoveAll(work))
	})
	if !t.Run("FileReadLayouts", TestIOTracingFileReadLayouts) {
		t.FailNow()
	}
	hasLoadedXFS := false
	t.Cleanup(func() {
		if !hasLoadedXFS {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for {
			_, err := ioTracingKernelCommand(cleanupCtx, "rmmod", "xfs")
			if err == nil {
				return
			}
			if cleanupCtx.Err() != nil {
				t.Errorf("cannot restore initially absent XFS module: %v", err)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	qualified := 0
	for _, filesystem := range []string{"ext4", "xfs"} {
		require.NoError(t, ctx.Err(), "iotracing qualification interrupted")
		if !t.Run(filesystem, func(t *testing.T) {
			if _, err := exec.LookPath("mkfs." + filesystem); err != nil {
				t.Skipf("%s qualification requires mkfs.%s", filesystem, filesystem)
			}
			filesystems, err := os.ReadFile("/proc/filesystems")
			require.NoError(t, err)
			if !slices.Contains(strings.Fields(string(filesystems)), filesystem) {
				if _, err := exec.LookPath("modprobe"); err != nil {
					t.Skipf("%s is unavailable in the running kernel", filesystem)
				}
				if _, err := ioTracingKernelCommand(ctx, "modprobe", "-n", "-q", filesystem); err != nil {
					t.Skipf("%s is unavailable: %v", filesystem, err)
				}
				if filesystem == "xfs" {
					_, err := exec.LookPath("rmmod")
					require.NoError(t, err)
					hasLoadedXFS = true
				}
			}
			captured := prepareIOTracingKernelFilesystem(
				t,
				ctx,
				work,
				filesystem,
				"captured",
			)
			excluded := prepareIOTracingKernelFilesystem(
				t,
				ctx,
				work,
				filesystem,
				"excluded",
			)
			t.Setenv("TMPDIR", captured)
			t.Setenv("TEST_IOTRACING_OTHER_FS", excluded)
			for _, test := range []struct {
				name string
				run  func(*testing.T)
			}{
				{
					name: "BlockDevice",
					run:  TestIOTracingBlockDevice,
				},
				{
					name: "FullObject",
					run:  TestIOTracingFullObject,
				},
				{
					name: "FilemapCapacity",
					run:  TestIOTracingFilemapCapacity,
				},
				{
					name: "FilemapDeviceFilter",
					run:  TestIOTracingFilemapDeviceFilter,
				},
				{
					name: "ReadWrite",
					run:  TestIOTracingReadWrite,
				},
				{
					name: "FileMetadata",
					run:  TestIOTracingFileMetadata,
				},
			} {
				require.NoError(t, ctx.Err(), "iotracing qualification interrupted")
				if !t.Run(test.name, test.run) {
					t.FailNow()
				}
			}
			require.NoError(t, ctx.Err(), "iotracing qualification interrupted")
			qualified++
		}) {
			t.FailNow()
		}
	}
	if qualified == 0 {
		t.Skip("iotracing qualification requires private ext4 or XFS filesystems")
	}
	require.NoError(t, ctx.Err(), "iotracing qualification interrupted")
}

func ioTracingKernelCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, name, args...)
	command.WaitDelay = time.Second
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf(
			"%s %s: %w: %s",
			name,
			strings.Join(args, " "),
			err,
			output,
		)
	}
	return output, nil
}

func prepareIOTracingKernelFilesystem(
	t *testing.T,
	ctx context.Context,
	work string,
	filesystem string,
	role string,
) string {
	t.Helper()
	directory := filepath.Join(work, filesystem+"-"+role)
	image := directory + ".img"
	require.NoError(t, os.Mkdir(directory, 0o700))
	t.Logf("owned filesystem: image=%s mount=%s", image, directory)
	// Register by backing identity before losetup can create a device and fail.
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		mounts, err := os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			t.Errorf(
				"cannot inspect mount %s; retain %s: %v",
				directory,
				image,
				err,
			)
			return
		}
		for _, line := range strings.Split(string(mounts), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 4 && fields[4] == directory {
				if _, err := ioTracingKernelCommand(cleanupCtx, "umount", directory); err != nil {
					t.Errorf(
						"cannot unmount owned filesystem %s; retain %s: %v",
						directory,
						image,
						err,
					)
					return
				}
			}
		}
		devices, err := ioTracingKernelCommand(
			cleanupCtx,
			"losetup",
			"-j",
			image,
			"-n",
			"-O",
			"NAME",
		)
		if err != nil {
			t.Errorf("cannot inspect owned loop backing %s: %v", image, err)
			return
		}
		for _, device := range strings.Fields(string(devices)) {
			if _, err := ioTracingKernelCommand(cleanupCtx, "losetup", "-d", device); err != nil {
				t.Errorf(
					"cannot detach owned loop %s backing=%s: %v",
					device,
					image,
					err,
				)
				return
			}
			for {
				remaining, err := ioTracingKernelCommand(
					cleanupCtx,
					"losetup",
					"-j",
					image,
					"-n",
					"-O",
					"NAME",
				)
				if err != nil {
					t.Errorf(
						"cannot confirm loop release: %s backing=%s: %v",
						device,
						image,
						err,
					)
					return
				}
				if !slices.Contains(strings.Fields(string(remaining)), device) {
					break
				}
				if cleanupCtx.Err() != nil {
					t.Errorf("owned loop remains: %s backing=%s", device, image)
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	})
	file, err := os.OpenFile(image, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	require.NoError(t, err)
	size := int64(64 << 20)
	format := []string{"-q", "-F"}
	if filesystem == "xfs" {
		size = 512 << 20
		format = []string{"-q", "-f"}
		usage, _ := ioTracingKernelCommand(ctx, "mkfs.xfs")
		for _, feature := range []string{"inobtcount", "bigtime"} {
			if strings.Contains(string(usage), feature+"=") {
				format = append(format, "-m", feature+"=0")
			}
		}
		for _, feature := range []string{"nrext64", "exchange"} {
			if strings.Contains(string(usage), feature+"=") {
				format = append(format, "-i", feature+"=0")
			}
		}
		if strings.Contains(string(usage), "parent=") {
			format = append(format, "-n", "parent=0")
		}
	}
	err = file.Truncate(size)
	closeErr := file.Close()
	require.NoError(t, err)
	require.NoError(t, closeErr)
	output, err := ioTracingKernelCommand(
		ctx,
		"losetup",
		"--find",
		"--show",
		image,
	)
	require.NoError(t, err)
	device := strings.TrimSpace(string(output))
	format = append(format, device)
	_, err = ioTracingKernelCommand(ctx, "mkfs."+filesystem, format...)
	require.NoError(t, err)
	_, err = ioTracingKernelCommand(
		ctx,
		"mount",
		device,
		directory,
	)
	require.NoError(t, err)
	return directory
}

// TestIOTracingBlockDevice observes real file IO on an ext4/XFS TMPDIR.
func TestIOTracingBlockDevice(t *testing.T) {
	object := os.Getenv("TEST_IOTRACING_FILE_BPF")
	require.NotEmpty(t, object, "run make test to supply the production BPF object")
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

	t.Run("block_device_inode", func(t *testing.T) {
		buffer := make([]byte, os.Getpagesize())
		device := filepath.Join(dir, "block-device-node")
		require.NoError(t, unix.Mknod(device, unix.S_IFBLK|0o600, int(stat.Dev)))
		fd, err := unix.Open(device, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = unix.Close(fd) })
		size, err := unix.Seek(fd, 0, unix.SEEK_END)
		require.NoError(t, err)
		require.GreaterOrEqual(t, size, int64(len(buffer)))
		offset := (size/int64(len(buffer)) - 1) * int64(len(buffer))
		// Buffered device IO uses a bdevfs inode whose superblock is not the
		// request device. Evict only this page so the read reaches block IO.
		require.NoError(t, unix.Fadvise(
			fd,
			offset,
			int64(len(buffer)),
			unix.FADV_DONTNEED,
		))
		coll := load(t, object, []uint32{fileKey.Dev})
		n, err := unix.Pread(fd, buffer, offset)
		require.NoError(t, err)
		require.Equal(t, len(buffer), n)
		require.Eventually(
			t,
			func() bool {
				var key sourceKey
				var record bpfFilesystemIO
				entries := coll.Maps[bpfSourceMapName].Iterate()
				for entries.Next(&key, &record) {
					hasDeviceIdentity := key.Dev == fileKey.Dev && record.DevID == fileKey.Dev
					hasProcessIdentity := record.TGID == uint32(os.Getpid())
					hasCompletedRead := record.Latency.Count > 0 && record.BlockReadBytes >= uint64(len(buffer))
					if hasDeviceIdentity && hasProcessIdentity && hasCompletedRead {
						return true
					}
				}
				require.NoError(t, entries.Err())
				return false
			},
			3*time.Second,
			10*time.Millisecond,
			"buffered block-device IO must retain request device %d",
			fileKey.Dev,
		)
	})

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
	require.NotEmpty(t, object, "run make test to supply the production BPF object")
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
	reader, err := attachAndEventPipe(ctx, &ioTracingProbeQualifier{BPF: program})
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
			return fmt.Errorf(
				"missing symbol %q in %s after (%w)",
				symbol,
				path,
				attachErr,
			)
		}
		if len(entries) != 1 {
			return fmt.Errorf(
				"multiple entries for %q in %s after (%w)",
				symbol,
				path,
				attachErr,
			)
		}
		fields := strings.Fields(entries[0])
		if len(fields) != 3 && len(fields) != 4 {
			return fmt.Errorf(
				"malformed symbol %q in %s after (%w)",
				symbol,
				path,
				attachErr,
			)
		}
		if fields[1] != "t" && fields[1] != "T" {
			return fmt.Errorf(
				"symbol %q is not text in %s after (%w)",
				symbol,
				path,
				attachErr,
			)
		}
		address, err := strconv.ParseUint(fields[0], 16, 64)
		if err != nil {
			return fmt.Errorf(
				"parse %q address after (%w): %w",
				symbol,
				attachErr,
				err,
			)
		}
		if address == 0 {
			return fmt.Errorf(
				"hidden address for %q in %s after (%w)",
				symbol,
				path,
				attachErr,
			)
		}
		if len(fields) != 4 || fields[3] != "[xfs]" {
			return fmt.Errorf("symbol %q is not in the XFS module after (%w)", symbol, attachErr)
		}
	}
	// This proves name-based attachment is unavailable, not the kernel bug's cause.
	return fmt.Errorf(
		"%w: %s: %w",
		errXFSKprobeUnsupported,
		name,
		attachErr,
	)
}
