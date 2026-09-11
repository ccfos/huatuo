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
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	maxProcMapEntries   = 1 << 18
	maxELFMetadataBytes = 32 << 20
	maxELFSymbols       = 1 << 20
)

type addressRange struct {
	start uint64
	end   uint64
}

type vmImage struct {
	javaVersion string
	vmRelease   string
	symbols     map[string]uint64
	readable    []addressRange
}

const maxJavaReleaseBytes = 64 << 10

func (image *vmImage) displayVersion() string {
	if image == nil {
		return ""
	}
	if image.javaVersion != "" {
		return image.javaVersion
	}
	return image.vmRelease
}

func (image *vmImage) contains(address, size uint64) bool {
	if image == nil || len(image.readable) == 0 {
		return true
	}
	end, ok := checkedAdd(address, size)
	if !ok {
		return false
	}
	index := sort.Search(len(image.readable), func(index int) bool {
		return image.readable[index].end > address
	})
	return index < len(image.readable) && image.readable[index].start <= address &&
		end <= image.readable[index].end
}

func discoverVM(ctx context.Context, procRoot string, pid int) (*vmImage, error) {
	if procRoot == "" {
		procRoot = "/proc"
	}
	mapsPath := filepath.Join(procRoot, strconv.Itoa(pid), "maps")
	mappings, err := memsnap.ReadProcMapsContext(ctx, mapsPath, maxProcMapEntries)
	if err != nil {
		return nil, fmt.Errorf("read HotSpot maps: %w", err)
	}
	readable := make([]addressRange, 0, len(mappings))
	for _, mapping := range mappings {
		if !strings.HasPrefix(mapping.Perms, "r") {
			continue
		}
		if len(readable) >= maxProcMapEntries {
			return nil, unsupportedHotSpot(
				"readable mapping count exceeds safety limit")
		}
		readable = append(readable, addressRange{
			start: mapping.Start, end: mapping.End,
		})
	}
	var mappedPath string
	var mappedInode uint64
	for _, mapping := range mappings {
		path := strings.TrimSuffix(mapping.Path, " (deleted)")
		if !strings.HasSuffix(path, "/libjvm.so") {
			continue
		}
		mappedPath = path
		mappedInode = mapping.Inode
		break
	}
	if mappedPath == "" {
		return nil, fmt.Errorf("%w: target does not map libjvm.so",
			errHotSpotUnavailable)
	}
	imagePath := filepath.Join(procRoot, strconv.Itoa(pid), "root", mappedPath)
	imageFile, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("open target libjvm.so: %w", err)
	}
	defer imageFile.Close()
	file, err := memsnap.ReadELFMetadata(ctx, imageFile)
	if err != nil {
		return nil, fmt.Errorf("read target libjvm.so ELF metadata: %w", err)
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.ByteOrder != binary.LittleEndian {
		return nil, fmt.Errorf("%w: unsupported ELF class or byte order",
			errHotSpotUnavailable)
	}
	loadBias, err := memsnap.FindELFLoadBias(file, mappings, mappedInode)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot determine libjvm.so load bias",
			errHotSpotUnavailable)
	}
	dynamicSymbols, err := memsnap.ReadELFSymbols(ctx, file, elf.SHT_DYNSYM,
		maxELFMetadataBytes, maxELFSymbols, func(name string) bool {
			return strings.HasPrefix(name, "gHotSpotVM")
		})
	if err != nil {
		return nil, unsupportedHotSpot(
			"libjvm.so dynamic symbols are unavailable")
	}
	symbols := make(map[string]uint64, len(dynamicSymbols))
	for _, symbol := range dynamicSymbols {
		name := strings.SplitN(symbol.Name, "@", 2)[0]
		if strings.HasPrefix(name, "gHotSpotVM") {
			address, valid := checkedAdd(loadBias, symbol.Value)
			if valid {
				symbols[name] = address
			}
		}
	}
	version, err := readJavaVersion(ctx, procRoot, pid, mappedPath)
	if err != nil {
		return nil, fmt.Errorf("read Java release: %w", err)
	}
	return &vmImage{
		javaVersion: version, symbols: symbols, readable: readable,
	}, nil
}

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
// candidates must not prevent capture; displayVersion can use the VM release.
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
