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

package memsnapshot

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ccfos/huatuo/internal/procfs"
)

const (
	maxProcMapsBytes   = 16 << 20
	maxProcMapsLine    = 64 << 10
	defaultMaxProcMaps = 1 << 18
)

// ReadProcessInstance reads the process identity from the default procfs mount.
// The raw start-time tick count is preserved, including zero.
func ReadProcessInstance(pid int) (ProcessInstance, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return ProcessInstance{}, fmt.Errorf("open procfs: %w", err)
	}
	process, err := fs.Proc(pid)
	if err != nil {
		return ProcessInstance{}, fmt.Errorf("open process %d: %w", pid, err)
	}
	stat, err := process.Stat()
	if err != nil {
		return ProcessInstance{}, fmt.Errorf("read process %d stat: %w", pid, err)
	}
	return ProcessInstance{TGID: pid, StartTimeTicks: stat.Starttime}, nil
}

// ValidateProcessInstance rejects an invalid identity or a reused PID.
func ValidateProcessInstance(identity ProcessInstance) error {
	current, err := ReadProcessInstance(identity.TGID)
	if err != nil {
		return err
	}
	if current.StartTimeTicks != identity.StartTimeTicks {
		return fmt.Errorf("process identity changed: start time %d, want %d", current.StartTimeTicks, identity.StartTimeTicks)
	}
	return nil
}

// ProcMap is the subset of one /proc/<pid>/maps entry used by runtime readers.
type ProcMap struct {
	DevMajor uint32
	DevMinor uint32
	Start    uint64
	End      uint64
	Offset   uint64
	Inode    uint64
	Perms    string
	Path     string
}

// ReadProcMaps reads and parses one process maps file. Malformed lines are
// ignored so a single racing or unknown mapping does not hide valid modules.
func ReadProcMaps(path string) ([]ProcMap, error) {
	return ReadProcMapsContext(context.Background(), path, defaultMaxProcMaps)
}

// ReadProcMapsContext streams a bounded maps file and stops promptly between
// lines when the capture context expires.
func ReadProcMapsContext(ctx context.Context, path string,
	maxEntries int,
) ([]ProcMap, error) {
	if maxEntries <= 0 {
		return nil, errors.New("process maps entry limit must be positive")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maxProcMapsBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), maxProcMapsLine)
	var result []ProcMap
	for lineNumber := 0; scanner.Scan(); lineNumber++ {
		if lineNumber&127 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		line := scanner.Text()
		// Split only the metadata; whitespace inside the pathname is significant.
		var fields [5]string
		for i := range fields {
			line = strings.TrimLeft(line, " \t")
			end := strings.IndexAny(line, " \t")
			if end < 0 {
				fields[i], line = line, ""
			} else {
				fields[i], line = line[:end], line[end:]
			}
		}
		if fields[4] == "" {
			continue
		}
		device := strings.SplitN(fields[3], ":", 2)
		if len(device) != 2 {
			continue
		}
		major, majorErr := strconv.ParseUint(device[0], 16, 32)
		minor, minorErr := strconv.ParseUint(device[1], 16, 32)
		addresses := strings.SplitN(fields[0], "-", 2)
		if len(addresses) != 2 {
			continue
		}
		start, startErr := strconv.ParseUint(addresses[0], 16, 64)
		end, endErr := strconv.ParseUint(addresses[1], 16, 64)
		offset, offsetErr := strconv.ParseUint(fields[2], 16, 64)
		inode, inodeErr := strconv.ParseUint(fields[4], 10, 64)
		if startErr != nil || endErr != nil || start >= end ||
			offsetErr != nil || inodeErr != nil || majorErr != nil || minorErr != nil {
			continue
		}
		mappedPath := strings.TrimLeft(line, " \t")
		result = append(result, ProcMap{
			Start: start, End: end, Offset: offset, Inode: inode,
			DevMajor: uint32(major), DevMinor: uint32(minor),
			Perms: fields[1], Path: mappedPath,
		})
		if len(result) > maxEntries {
			return nil, fmt.Errorf("process maps entry limit %d exceeded", maxEntries)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan process maps: %w", err)
	}
	if limited.N == 0 {
		return nil, fmt.Errorf("process maps size limit %d bytes exceeded",
			maxProcMapsBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// FindLoadBias resolves an ELF load bias from a parsed maps entry.
func FindLoadBias(mappings []ProcMap, target *ProcMap,
	loadOffset, loadAddress uint64,
) (uint64, error) {
	for index := range mappings {
		mapping := &mappings[index]
		if mapping.Inode == target.Inode && mapping.DevMajor == target.DevMajor &&
			mapping.DevMinor == target.DevMinor && mapping.Offset == loadOffset &&
			mapping.Start >= loadAddress {
			return mapping.Start - loadAddress, nil
		}
	}
	return 0, errors.New("ELF load bias not found")
}
