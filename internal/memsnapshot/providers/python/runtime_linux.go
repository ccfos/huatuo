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

package python

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ccfos/huatuo/internal/symbol"

	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/procfs"
)

const (
	maxRuntimeModules      = 64
	maxRuntimeModuleMaps   = 1024
	maxRuntimeFailureBytes = 4096
	maxELFMetadataBytes    = 32 << 20
	maxELFSymbols          = 1 << 20
)

type image struct {
	version        version
	runtimeAddress uint64
	layout         runtimeLayout
	order          binary.ByteOrder
}

type module struct {
	hostPath string
	maps     []memsnapshot.ProcMap
}

func dynamicSymbols(ctx context.Context, file *elf.File, names ...string) (map[string]elf.Symbol, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	symbols, err := symbol.ReadELFSymbols(ctx, file, elf.SHT_DYNSYM,
		maxELFMetadataBytes, maxELFSymbols, func(name string) bool {
			_, ok := wanted[name]
			return ok
		})
	// A module without a dynamic symbol table can be skipped during discovery.
	if err != nil && !errors.Is(err, elf.ErrNoSymbols) {
		return nil, fmt.Errorf("read CPython module symbols: %w", err)
	}
	result := make(map[string]elf.Symbol, len(names))
	for _, symbol := range symbols {
		if symbol.Section == elf.SHN_UNDEF {
			continue
		}
		if _, ok := wanted[symbol.Name]; ok {
			result[symbol.Name] = symbol
			if len(result) == len(wanted) {
				break
			}
		}
	}
	return result, nil
}

func findSymbol(symbols map[string]elf.Symbol, name string) (elf.Symbol, error) {
	if symbol, ok := symbols[name]; ok {
		return symbol, nil
	}
	return elf.Symbol{}, fmt.Errorf("ELF symbol %s is unavailable", name)
}

func discoverRuntime(ctx context.Context, procRoot string, pid int,
	memory memoryReader,
) (image, error) {
	if err := ctx.Err(); err != nil {
		return image{}, err
	}
	maps, err := memsnapshot.ReadProcMapsContext(ctx,
		filepath.Join(procRoot, strconv.Itoa(pid), "maps"), maxRuntimeModuleMaps*maxRuntimeModules)
	if err != nil {
		return image{}, fmt.Errorf("read CPython victim maps: %w", err)
	}
	candidates, candidateWarning := runtimeModules(procRoot, pid, maps)
	var failures []string
	var causes []error
	failureBytes := 0
	failuresOmitted := false
	appendFailure := func(reason string) {
		separatorBytes := 0
		if len(failures) != 0 {
			separatorBytes = 2
		}
		remaining := maxRuntimeFailureBytes - failureBytes - separatorBytes
		if remaining <= 0 {
			failuresOmitted = true
			return
		}
		if len(reason) > remaining {
			reason = strings.ToValidUTF8(reason[:remaining], "")
			failuresOmitted = true
		}
		failures = append(failures, reason)
		failureBytes += separatorBytes + len(reason)
	}
	nonCPythonModules := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return image{}, err
		}
		result, imageErr := inspectModule(ctx, candidate.hostPath,
			candidate.maps, memory)
		if imageErr == nil {
			return result, nil
		}
		if errors.Is(imageErr, errNotCPythonModule) {
			nonCPythonModules++
			continue
		}
		if errors.Is(imageErr, errUnsupportedRuntime) {
			return image{}, imageErr
		}
		causes = append(causes, imageErr)
		appendFailure(imageErr.Error())
	}
	if candidateWarning != "" {
		causes = append(causes, errors.New(candidateWarning))
		appendFailure(candidateWarning)
	}
	if nonCPythonModules == len(candidates) && candidateWarning == "" {
		return image{}, fmt.Errorf("%w: no mapped module exports _PyRuntime",
			errUnsupportedRuntime)
	}
	if len(failures) == 0 {
		return image{}, fmt.Errorf("%w: no inspectable CPython runtime module",
			errUnsupportedRuntime)
	}
	reason := strings.Join(failures, "; ")
	if failuresOmitted {
		reason += "; additional module failures omitted"
	}
	return image{}, boundedError{cause: errors.Join(causes...), reason: "locate CPython _PyRuntime: " + reason}
}

func runtimeModules(procRoot string, pid int,
	maps []memsnapshot.ProcMap,
) ([]module, string) {
	type moduleKey [2]string
	byKey := make(map[moduleKey][]memsnapshot.ProcMap)
	var order []moduleKey
	limitReached := false
	executablePath := filepath.Join(procRoot, strconv.Itoa(pid), "exe")
	executableTarget, _ := os.Readlink(executablePath)
	executableTarget = procfs.TrimDeletedSuffix(executableTarget)
	for _, mapping := range maps {
		if mapping.Inode == 0 || mapping.Path == "" || strings.HasPrefix(mapping.Path, "[") {
			continue
		}
		path := procfs.TrimDeletedSuffix(mapping.Path)
		isExecutable := executableTarget != "" && path == executableTarget
		isLibPython := strings.HasPrefix(strings.ToLower(filepath.Base(path)),
			"libpython")
		if !isExecutable && !isLibPython {
			continue
		}
		key := moduleKey{strconv.FormatUint(mapping.Inode, 10), path}
		if _, ok := byKey[key]; !ok {
			if len(order) >= maxRuntimeModules {
				limitReached = true
				continue
			}
			order = append(order, key)
		}
		if len(byKey[key]) >= maxRuntimeModuleMaps {
			limitReached = true
			continue
		}
		byKey[key] = append(byKey[key], mapping)
	}
	modules := []module{{
		hostPath: executablePath,
	}}
	for _, key := range order {
		group := byKey[key]
		if key[1] == executableTarget {
			modules[0].maps = group
			continue
		}
		if !strings.HasPrefix(strings.ToLower(filepath.Base(key[1])), "libpython") {
			continue
		}
		if len(modules) >= maxRuntimeModules {
			limitReached = true
			break
		}
		hostPath := runtimeModulePath(procRoot, pid, key[1], group)
		modules = append(modules, module{hostPath: hostPath, maps: group})
	}
	if limitReached {
		return modules, "CPython runtime module or mapping limit reached"
	}
	return modules, ""
}

func runtimeModulePath(procRoot string, pid int, path string,
	maps []memsnapshot.ProcMap,
) string {
	var deleted *memsnapshot.ProcMap
	for index := range maps {
		mapping := &maps[index]
		if !strings.HasSuffix(mapping.Path, " (deleted)") ||
			mapping.End <= mapping.Start {
			continue
		}
		if deleted == nil || mapping.Offset < deleted.Offset {
			deleted = mapping
		}
	}
	pidRoot := filepath.Join(procRoot, strconv.Itoa(pid))
	if deleted != nil {
		return filepath.Join(pidRoot, "map_files", fmt.Sprintf("%x-%x",
			deleted.Start, deleted.End))
	}
	return filepath.Join(pidRoot, "root", path)
}

func inspectModule(ctx context.Context, path string, maps []memsnapshot.ProcMap,
	memory memoryReader,
) (image, error) {
	if err := ctx.Err(); err != nil {
		return image{}, err
	}
	if len(maps) == 0 {
		return image{}, fmt.Errorf("no process mapping for %s", path)
	}
	moduleFile, err := memsnapshot.OpenMappedFile(path, maps[0])
	if err != nil {
		return image{}, err
	}
	defer moduleFile.Close()
	if err := ctx.Err(); err != nil {
		return image{}, err
	}
	file, err := elf.NewFile(moduleFile)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return image{}, ctxErr
	}
	if err != nil {
		return image{}, err
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.ByteOrder != binary.LittleEndian {
		return image{}, unsupportedRuntime(
			"CPython external reader requires little-endian ELF64",
		)
	}
	// Keep the synchronous OOM path focused on the observability symbols
	// that are actually consumed. Building an index for every ELF symbol costs
	// more than the sampling itself on some CPython 3.9/3.10 builds.
	dynamicSymbols, err := dynamicSymbols(ctx, file, "_PyRuntime", "Py_Version")
	if err != nil {
		return image{}, err
	}
	if err := ctx.Err(); err != nil {
		return image{}, err
	}
	runtimeSymbol, err := findSymbol(dynamicSymbols, "_PyRuntime")
	if err != nil {
		return image{}, fmt.Errorf("%w: %w", errNotCPythonModule, err)
	}
	bias := uint64(0)
	if file.Type == elf.ET_DYN {
		bias, err = loadBias(file, maps)
		if err != nil {
			return image{}, err
		}
	}
	// Reject malformed ELF symbols before relocation can wrap to mapped memory.
	if runtimeSymbol.Value > ^uint64(0)-bias {
		return image{}, errors.New("_PyRuntime address overflows load bias")
	}
	runtimeVersion := version{}
	if versionSymbol, versionErr := findSymbol(dynamicSymbols, "Py_Version"); versionErr == nil {
		if versionSymbol.Value > ^uint64(0)-bias {
			return image{}, errors.New("Py_Version address overflows load bias")
		}
		versionRaw, readErr := memory.read(bias+versionSymbol.Value, 4)
		if readErr != nil {
			return image{}, fmt.Errorf("read Py_Version: %w", readErr)
		}
		packed := file.ByteOrder.Uint32(versionRaw)
		runtimeVersion = version{
			major: int(packed >> 24),
			minor: int((packed >> 16) & 0xff), micro: int((packed >> 8) & 0xff),
			microKnown: true,
		}
		if runtimeVersion.major != 3 {
			return image{}, unsupportedRuntime(
				fmt.Sprintf("unexpected Py_Version %#x", packed),
			)
		}
	} else {
		runtimeVersion, err = versionFromModulePath(path)
		if err != nil {
			return image{}, fmt.Errorf("%w: %w", errUnsupportedRuntime, err)
		}
	}
	layout, err := layoutFor(runtimeVersion)
	if err != nil {
		return image{}, err
	}
	return image{
		version: runtimeVersion, layout: layout,
		runtimeAddress: bias + runtimeSymbol.Value,
		order:          file.ByteOrder,
	}, nil
}

func versionFromModulePath(path string) (version, error) {
	name := strings.ToLower(filepath.Base(path))
	marker := strings.Index(name, "python3.")
	if marker < 0 {
		return version{}, errors.New("Py_Version and a versioned libpython name are unavailable")
	}
	minorText := name[marker+len("python3."):]
	end := 0
	for end < len(minorText) && minorText[end] >= '0' && minorText[end] <= '9' {
		end++
	}
	if end == 0 {
		return version{}, errors.New("libpython name has no minor version")
	}
	minor, err := strconv.Atoi(minorText[:end])
	if err != nil {
		return version{}, err
	}
	return version{major: 3, minor: minor}, nil
}

func loadBias(file *elf.File, maps []memsnapshot.ProcMap) (uint64, error) {
	if len(maps) == 0 {
		return 0, errors.New("CPython module has no process mappings")
	}
	bias, err := memsnapshot.FindELFLoadBias(file, maps, &maps[0])
	if err != nil {
		return 0, errors.New("cannot determine CPython module load bias")
	}
	return bias, nil
}
