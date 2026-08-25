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

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	maxRuntimeModules      = 64
	maxRuntimeModuleMaps   = 1024
	maxRuntimeFailureBytes = 4096
	maxELFMetadataBytes    = 32 << 20
	maxELFSymbols          = 1 << 20
)

type version struct {
	major      int
	minor      int
	micro      int
	microKnown bool
}

type image struct {
	version        version
	runtimeAddress uint64
	layout         runtimeLayout
	order          binary.ByteOrder
}

type module struct {
	hostPath string
	maps     []memsnap.ProcMap
}

type interpreterLayout uint8

const (
	layoutUnknown interpreterLayout = iota
	layoutRuntimeGC
	layoutProbedList
	layoutFixed
	layoutDebugOffsets
)

type runtimeLayout struct {
	interpreterMode     interpreterLayout
	runtimeHeadOffset   uint64
	interpreterGCOffset uint64
	debugInterpreterGC  uint64
	debugObjectType     uint64
	debugTypeName       uint64
	debugTypeFlags      uint64
	objectTypeOffset    uint64
	objectSizeOffset    uint64
	typeNameOffset      uint64
	typeFlagsOffset     uint64
	unicodeDataOffset   uint64
}

var runtimeLayouts = map[int]runtimeLayout{
	8: {
		interpreterMode: layoutRuntimeGC, unicodeDataOffset: 48,
	},
	9: {
		interpreterMode: layoutProbedList, unicodeDataOffset: 48,
	},
	10: {
		interpreterMode: layoutProbedList, unicodeDataOffset: 48,
	},
	11: {
		interpreterMode: layoutProbedList, unicodeDataOffset: 48,
	},
	12: {
		interpreterMode: layoutFixed, runtimeHeadOffset: 40,
		interpreterGCOffset: 112, unicodeDataOffset: 40,
	},
	13: {
		interpreterMode:    layoutDebugOffsets,
		debugInterpreterGC: 80, debugObjectType: 360,
		debugTypeName: 376, debugTypeFlags: 392, unicodeDataOffset: 40,
	},
	14: {
		interpreterMode:    layoutDebugOffsets,
		debugInterpreterGC: 88, debugObjectType: 408,
		debugTypeName: 424, debugTypeFlags: 440, unicodeDataOffset: 40,
	},
}

func (v version) String() string {
	if !v.microKnown {
		return fmt.Sprintf("%d.%d.x", v.major, v.minor)
	}
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.micro)
}

func dynamicSymbols(ctx context.Context, file *elf.File, names ...string) (map[string]elf.Symbol, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	symbols, err := memsnap.ReadELFSymbols(ctx, file, elf.SHT_DYNSYM,
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
	maps, err := memsnap.ReadProcMapsContext(ctx,
		filepath.Join(procRoot, strconv.Itoa(pid), "maps"), maxRuntimeModuleMaps*maxRuntimeModules)
	if err != nil {
		return image{}, fmt.Errorf("read CPython victim maps: %w", err)
	}
	candidates, candidateWarning := runtimeModules(procRoot, pid, maps)
	var failures []string
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
		appendFailure(imageErr.Error())
	}
	if candidateWarning != "" {
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
	return image{}, fmt.Errorf("locate CPython _PyRuntime: %s", reason)
}

func runtimeModules(procRoot string, pid int,
	maps []memsnap.ProcMap,
) ([]module, string) {
	type moduleKey [2]string
	byKey := make(map[moduleKey][]memsnap.ProcMap)
	order := make([]moduleKey, 0)
	limitReached := false
	executablePath := filepath.Join(procRoot, strconv.Itoa(pid), "exe")
	executableTarget, _ := os.Readlink(executablePath)
	executableTarget = strings.TrimSuffix(executableTarget, " (deleted)")
	for _, mapping := range maps {
		if mapping.Inode == 0 || mapping.Path == "" || strings.HasPrefix(mapping.Path, "[") {
			continue
		}
		path := strings.TrimSuffix(mapping.Path, " (deleted)")
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
	maps []memsnap.ProcMap,
) string {
	var deleted *memsnap.ProcMap
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

func inspectModule(ctx context.Context, path string, maps []memsnap.ProcMap,
	memory memoryReader,
) (image, error) {
	if err := ctx.Err(); err != nil {
		return image{}, err
	}
	moduleFile, err := os.Open(path)
	if err != nil {
		return image{}, err
	}
	defer moduleFile.Close()
	file, err := memsnap.ReadELFMetadata(ctx, moduleFile)
	if err != nil {
		return image{}, err
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.ByteOrder != binary.LittleEndian {
		return image{}, unsupportedRuntime(
			"CPython external reader requires little-endian ELF64")
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
				fmt.Sprintf("unexpected Py_Version %#x", packed))
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

func loadBias(file *elf.File, maps []memsnap.ProcMap) (uint64, error) {
	if len(maps) == 0 {
		return 0, errors.New("CPython module has no process mappings")
	}
	bias, err := memsnap.FindELFLoadBias(file, maps, maps[0].Inode)
	if err != nil {
		return 0, errors.New("cannot determine CPython module load bias")
	}
	return bias, nil
}

func layoutFor(version version) (runtimeLayout, error) {
	layout, ok := runtimeLayouts[version.minor]
	if version.major != 3 || !ok {
		return runtimeLayout{}, fmt.Errorf("%w: %s", errUnsupportedRuntime,
			version.String())
	}
	layout.objectTypeOffset = pyObjectTypeOffset
	layout.objectSizeOffset = pyObjectSizeOffset
	layout.typeNameOffset = pyTypeNameOffset
	layout.typeFlagsOffset = pyTypeFlagsOffset
	return layout, nil
}
