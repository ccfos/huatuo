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

package main

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type fileResultKey struct {
	PID   uint32
	Dev   uint32
	Inode uint64
}

func attachFileResultProbes(t *testing.T, path string) (*ebpf.Collection, fileResultKey) {
	t.Helper()
	require.Equal(t, 0, os.Geteuid(), "file-result qualification requires root")
	object := os.Getenv("TEST_IOTRACING_FILE_BPF")
	require.NotEmpty(t, object, "supply the production iotracing BPF object")
	var stat unix.Stat_t
	require.NoError(t, unix.Stat(path, &stat))
	var fs unix.Statfs_t
	require.NoError(t, unix.Statfs(path, &fs))
	var prefix string
	switch fs.Type {
	case unix.EXT4_SUPER_MAGIC:
		prefix = "ext4"
	case unix.XFS_SUPER_MAGIC:
		prefix = "xfs"
	default:
		t.Fatal("file-result qualification requires ext4 or XFS")
	}
	key := fileResultKey{Dev: unix.Major(stat.Dev)<<20 | unix.Minor(stat.Dev), Inode: stat.Ino}
	require.NoError(t, unix.Setrlimit(unix.RLIMIT_MEMLOCK, &unix.Rlimit{
		Cur: unix.RLIM_INFINITY, Max: unix.RLIM_INFINITY,
	}))
	spec, err := ebpf.LoadCollectionSpec(object)
	require.NoError(t, err)
	for name := range spec.Programs {
		if name != "bpf_anyfs_file_read_iter" && name != "bpf_anyfs_file_write_iter" &&
			name != "bpf_anyfs_file_read_iter_return" && name != "bpf_anyfs_file_write_iter_return" {
			delete(spec.Programs, name)
		}
	}
	require.NoError(t, spec.RewriteConstants(map[string]any{
		"FILTER_DEV_COUNT": uint32(1),
		"FILTER_DEV_IDS":   [16]uint32{key.Dev},
	}))
	coll, err := ebpf.NewCollection(spec)
	require.NoError(t, err)
	t.Cleanup(coll.Close)
	for _, operation := range []string{"read", "write"} {
		program := "bpf_anyfs_file_" + operation + "_iter"
		symbol := prefix + "_file_" + operation + "_iter"
		attachFileIOReturnProbe(t, program+"_return", symbol, coll.Programs[program+"_return"])
		attachFileIOProbe(t, symbol, coll.Programs[program])
	}
	return coll, key
}

func TestIOTracingFileReadResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read-results")
	require.NoError(t, os.WriteFile(path, []byte("12345678"), 0o600))
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	coll, key := attachFileResultProbes(t, path)
	buffer := make([]byte, 4096)
	n, err := unix.Pread(int(file.Fd()), buffer, 0)
	require.NoError(t, err)
	require.Equal(t, 8, n)
	n, err = unix.Pread(int(file.Fd()), buffer, 8)
	require.NoError(t, err)
	require.Zero(t, n)

	direct, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECT, 0)
	require.NoError(t, err)
	defer unix.Close(direct)
	_, err = unix.Pread(direct, buffer[:8], 1)
	require.ErrorIs(t, err, unix.EINVAL, "unaligned direct read must fail in the filesystem")
	var record bpfFilesystemIO
	require.NoError(t, coll.Maps[bpfSourceMapName].Lookup(key, &record))
	require.Equal(t, uint64(8), record.FsReadBytes, "short read, EOF and failed read")
	require.Zero(t, record.FsWriteBytes)
	var pendingKey uint64
	var pendingValue [32]byte
	pending := coll.Maps["file_io_start_map"].Iterate()
	for pending.Next(&pendingKey, &pendingValue) {
		require.NotEqual(t, uint64(record.TGID), pendingKey>>32,
			"completed calls must release their pending entries")
	}
	require.NoError(t, pending.Err())
}

func TestIOTracingConcurrentFileReadResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent-read-results")
	require.NoError(t, os.WriteFile(path, []byte("12345678"), 0o600))
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	coll, key := attachFileResultProbes(t, path)
	const workers, reads = 16, 100
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buffer := make([]byte, 4096)
			for range reads {
				n, err := unix.Pread(int(file.Fd()), buffer, 0)
				if err != nil || n != 8 {
					t.Errorf("Pread = %d, %v; want 8, nil", n, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	var record bpfFilesystemIO
	require.NoError(t, coll.Maps[bpfSourceMapName].Lookup(key, &record))
	require.Equal(t, uint64(workers*reads*8), record.FsReadBytes)
}

func TestIOTracingFileWriteResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "write-results")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	coll, key := attachFileResultProbes(t, path)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestIOTracingFileWriteLimitHelper$")
	cmd.Env = append(os.Environ(), "TEST_IOTRACING_WRITE_LIMIT_FILE="+path)
	result, err := cmd.CombinedOutput()
	require.NoError(t, err, "bounded writer: %s", result)
	var record bpfFilesystemIO
	require.NoError(t, coll.Maps[bpfSourceMapName].Lookup(key, &record))
	require.Equal(t, uint64(1024), record.FsWriteBytes, "short write and EFBIG")
	require.Zero(t, record.FsReadBytes)
}

func TestIOTracingFileWriteLimitHelper(t *testing.T) {
	path := os.Getenv("TEST_IOTRACING_WRITE_LIMIT_FILE")
	if path == "" {
		return
	}
	signal.Ignore(syscall.SIGXFSZ)
	require.NoError(t, unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 1024, Max: 1024}))
	fd, err := unix.Open(path, unix.O_WRONLY, 0)
	require.NoError(t, err)
	defer unix.Close(fd)
	n, err := unix.Pwrite(fd, make([]byte, 4096), 0)
	require.NoError(t, err)
	require.Equal(t, 1024, n)
	_, err = unix.Pwrite(fd, make([]byte, 4096), 1024)
	require.ErrorIs(t, err, unix.EFBIG)
}
