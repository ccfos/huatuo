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
	mapping, err := executableMapping(exePath, inode, mappings)
	if err != nil {
		return 0, err
	}
	offset, vaddr, err := firstLoadSegment(file)
	if err != nil {
		return 0, err
	}
	return memsnapshot.FindLoadBias(mappings, mapping, offset, vaddr)
}

// Use the executable pathname and inode to select its maps identity. Device
// numbers come from maps because Btrfs may report a different device in stat.
func executableMapping(exePath string, inode uint64, mappings []memsnapshot.ProcMap) (*memsnapshot.ProcMap, error) {
	path, err := os.Readlink(exePath)
	if err != nil {
		return nil, fmt.Errorf("read executable path: %w", err)
	}
	var selected *memsnapshot.ProcMap
	for index := range mappings {
		mapping := &mappings[index]
		if mapping.Inode != inode || mapping.Path != path {
			continue
		}
		if selected != nil && (mapping.DevMajor != selected.DevMajor || mapping.DevMinor != selected.DevMinor) {
			return nil, errors.New("executable maps identity is ambiguous")
		}
		selected = mapping
	}
	if selected == nil {
		return nil, errors.New("executable mapping not found")
	}
	return selected, nil
}

func firstLoadSegment(file *elf.File) (uint64, uint64, error) {
	pageSize := uint64(os.Getpagesize())
	for _, program := range file.Progs {
		if program.Type == elf.PT_LOAD {
			return alignDown(program.Off, pageSize), alignDown(program.Vaddr, pageSize), nil
		}
	}
	return 0, 0, errors.New("ELF has no PT_LOAD segment")
}

func alignDown(value, alignment uint64) uint64 {
	return value &^ (alignment - 1)
}
