// Copyright 2022-2025 The Parca Authors
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
//
// This file contains work derived from github.com/parca-dev/oomprof.
// It was modified by The HuaTuo Authors for integration with HuaTuo.

package golang

import (
	"context"
	"debug/buildinfo"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ccfos/huatuo/internal/elfmapping"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/utils/fileutil"
)

const defaultProcRoot = "/proc"

type processReader struct {
	executable *os.File
	elfFile    *elf.File
	runtime    *runtimeInfo
	memory     processMemory
}

func (r *processReader) Close() error {
	return errors.Join(r.elfFile.Close(), r.executable.Close())
}

// newProcessReader keeps the inspected executable pinned until symbolization finishes.
func newProcessReader(ctx context.Context,
	identity memsnapshot.ProcessInstanceID,
) (*processReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pid := identity.TGID
	if err := memsnapshot.ValidateProcessInstanceID(identity); err != nil {
		return nil, err
	}

	exePath := filepath.Join(defaultProcRoot, strconv.Itoa(pid), "exe")
	executable, err := os.Open(exePath)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			executable.Close()
		}
	}()

	file, err := elf.NewFile(executable)
	if err != nil {
		return nil, err
	}
	build, err := buildinfo.Read(executable)
	if err != nil {
		return nil, fmt.Errorf("%w: read Go build info: %w", errUnsupportedRuntime, err)
	}
	info, err := newRuntimeInfo(ctx, pid, file, build.GoVersion)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	keep = true
	return &processReader{
		executable: executable,
		elfFile:    file,
		runtime:    info,
		memory:     processMemory{pid: pid},
	}, nil
}

// resolveExecutableLoadBias reads /proc mappings only for position-independent executables.
func resolveExecutableLoadBias(ctx context.Context, pid int, file *elf.File) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if file.Type != elf.ET_DYN {
		return 0, nil
	}
	exePath := filepath.Join(defaultProcRoot, strconv.Itoa(pid), "exe")
	inode, err := fileutil.StatInode(exePath)
	if err != nil {
		return 0, err
	}
	mappings, err := memsnapshot.ReadProcMapsContext(ctx,
		filepath.Join(defaultProcRoot, strconv.Itoa(pid), "maps"), 1<<18)
	if err != nil {
		return 0, err
	}
	return executableLoadBias(exePath, inode, mappings, elfmapping.LoadSegments(file))
}

// File identity alone does not identify the main load instance. Without an
// authoritative loader address, reject multiple candidate biases instead of
// borrowing relocation from a second mmap of the executable.
func executableLoadBias(exePath string, inode uint64, mappings []memsnapshot.ProcMap,
	segments []elf.ProgHeader,
) (uint64, error) {
	path, err := os.Readlink(exePath)
	if err != nil {
		return 0, fmt.Errorf("read executable path: %w", err)
	}
	var selected *memsnapshot.ProcMap
	var bias uint64
	found := false
	var segmentErr error
	for index := range mappings {
		mapping := &mappings[index]
		if mapping.Inode != inode || mapping.Path != path {
			continue
		}
		if selected != nil && (mapping.DevMajor != selected.DevMajor || mapping.DevMinor != selected.DevMinor) {
			return 0, errors.New("executable maps identity is ambiguous")
		}
		selected = mapping
		candidate, err := elfmapping.LoadBias(segments, mapping.Start, mapping.Offset, elfmapping.MappingFlags(mapping.Perms))
		if err != nil {
			// A shared boundary page can belong to more than one PT_LOAD
			// (RELRO). It cannot nominate a bias; other mappings must agree.
			segmentErr = err
			continue
		}
		if found && candidate != bias {
			return 0, errors.New("executable load bias is ambiguous: multiple mapping instances")
		}
		bias, found = candidate, true
	}
	if selected == nil {
		return 0, errors.New("executable mapping not found")
	}
	if !found {
		return 0, fmt.Errorf("determine executable load bias: %w", segmentErr)
	}
	return bias, nil
}
