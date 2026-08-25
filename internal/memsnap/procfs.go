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

package memsnap

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
)

const (
	maxProcMapsBytes   = 16 << 20
	maxProcMapsLine    = 64 << 10
	defaultMaxProcMaps = 1 << 18
)

// ReadIdentity reads the stable identity from /proc/<pid>/stat.
func ReadIdentity(pid int) (ProcessIdentity, error) {
	if pid <= 0 {
		return ProcessIdentity{}, errors.New("pid must be greater than zero")
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("read process stat: %w", err)
	}
	starttime, err := ParseProcStatStartTime(stat)
	if err != nil {
		return ProcessIdentity{}, err
	}
	identity := ProcessIdentity{TGID: pid, StartTimeTicks: starttime}
	return identity, nil
}

// ValidateIdentity rejects an invalid identity or a reused PID.
func ValidateIdentity(procRoot string, identity ProcessIdentity) error {
	if identity.TGID <= 0 || identity.StartTimeTicks == 0 {
		return errors.New("process identity is invalid")
	}
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(identity.TGID),
		"stat"))
	if err != nil {
		return fmt.Errorf("read process identity: %w", err)
	}
	startTime, err := ParseProcStatStartTime(stat)
	if err != nil {
		return fmt.Errorf("parse process identity: %w", err)
	}
	if startTime != identity.StartTimeTicks {
		return fmt.Errorf("process identity changed: start time %d, want %d",
			startTime, identity.StartTimeTicks)
	}
	return nil
}

// ParseProcStatStartTime handles spaces and parentheses in comm by parsing
// fields only after the final ')' delimiter. starttime is proc field 22.
func ParseProcStatStartTime(stat []byte) (uint64, error) {
	closing := strings.LastIndexByte(string(stat), ')')
	if closing < 0 || closing+1 >= len(stat) {
		return 0, errors.New("process stat has no comm terminator")
	}
	fields := strings.Fields(string(stat[closing+1:]))
	// The first suffix field is field 3 (state), so starttime is index 19.
	if len(fields) <= 19 {
		return 0, fmt.Errorf("process stat has %d suffix fields, need at least 20", len(fields))
	}
	starttime, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse process starttime %q: %w", fields[19], err)
	}
	if starttime == 0 {
		return 0, errors.New("process starttime must be greater than zero")
	}
	return starttime, nil
}

// ProcMap is the subset of one /proc/<pid>/maps entry used by runtime readers.
type ProcMap struct {
	Start  uint64
	End    uint64
	Offset uint64
	Inode  uint64
	Perms  string
	Path   string
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
	if ctx == nil {
		ctx = context.Background()
	}
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
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		addresses := strings.SplitN(fields[0], "-", 2)
		if len(addresses) != 2 {
			continue
		}
		start, startErr := strconv.ParseUint(addresses[0], 16, 64)
		end, endErr := strconv.ParseUint(addresses[1], 16, 64)
		offset, offsetErr := strconv.ParseUint(fields[2], 16, 64)
		inode, inodeErr := strconv.ParseUint(fields[4], 10, 64)
		if startErr != nil || endErr != nil || start >= end ||
			offsetErr != nil || inodeErr != nil {
			continue
		}
		mappedPath := ""
		if len(fields) > 5 {
			mappedPath = strings.Join(fields[5:], " ")
		}
		result = append(result, ProcMap{
			Start: start, End: end, Offset: offset, Inode: inode,
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
func FindLoadBias(mappings []ProcMap, inode, loadOffset,
	loadAddress uint64,
) (uint64, error) {
	for _, mapping := range mappings {
		if mapping.Inode == inode && mapping.Offset == loadOffset &&
			mapping.Start >= loadAddress {
			return mapping.Start - loadAddress, nil
		}
	}
	return 0, errors.New("ELF load bias not found")
}
