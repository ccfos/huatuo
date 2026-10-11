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

package java

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const maxJavaReleaseBytes = 64 << 10

// Reopen only an already validated regular inode; no target pathname is followed.
func reopenPinnedRegular(pinned *os.File) (*os.File, error) {
	path := fmt.Sprintf("/proc/self/fd/%d", pinned.Fd())
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), pinned.Name()), nil
}

// openJavaRelease walks beneath the pinned target root, rejecting symlinks and
// special files. O_PATH pins the inode without opening a device or FIFO for I/O. Regular
// filesystem I/O can still block in the kernel; this is not a hard deadline.
func openJavaRelease(ctx context.Context, root, relative string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, "../") {
		return nil, errors.New("Java release path escapes target root")
	}
	fd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for index, part := range parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if index == len(parts)-1 {
			releaseFD, err := unix.Openat(fd, part, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				return nil, err
			}
			file := os.NewFile(uintptr(releaseFD), relative)
			stat, err := file.Stat()
			if err == nil && !stat.Mode().IsRegular() {
				err = errors.New("Java release is not a regular file")
			}
			if err == nil && stat.Size() > maxJavaReleaseBytes {
				err = errors.New("Java release exceeds byte budget")
			}
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			readable, err := reopenPinnedRegular(file)
			_ = file.Close()
			return readable, err
		}
		next, err := unix.Openat(fd, part, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil, errors.New("empty Java release path")
}

// The release file only supplements display metadata. Unreadable or rejected
// candidates must not prevent snapshot; displayVersion can use the VM release.
// Cancellation still terminates discovery instead of becoming a missing version.
func readJavaVersion(ctx context.Context, procRoot string, pid int, libjvmPath string) (string, error) {
	root := filepath.Join(procRoot, strconv.Itoa(pid), "root")
	directory := filepath.Dir(libjvmPath)
	for depth := 0; depth < 6; depth++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		relative := filepath.Join(strings.TrimPrefix(directory, "/"), "release")
		release, err := openJavaRelease(ctx, root, relative)
		if err == nil {
			version, readErr := parseJavaRelease(ctx, release)
			_ = release.Close()
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if readErr == nil && version != "" {
				return version, nil
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return "", ctx.Err()
}

func parseJavaRelease(ctx context.Context, reader io.Reader) (string, error) {
	limited := &io.LimitedReader{R: reader, N: maxJavaReleaseBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 1024), 4096)
	version := ""
	lines := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !scanner.Scan() {
			break
		}
		lines++
		if lines > 256 || limited.N == 0 {
			return "", errors.New("Java release exceeds metadata budget")
		}
		key, value, found := strings.Cut(scanner.Text(), "=")
		if found && key == "JAVA_VERSION" {
			version = strings.Trim(value, "\"")
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if limited.N == 0 {
		return "", errors.New("Java release exceeds byte budget")
	}
	return version, ctx.Err()
}
