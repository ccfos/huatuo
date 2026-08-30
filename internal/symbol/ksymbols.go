// Copyright 2025, 2026 The HuaTuo Authors
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

package symbol

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
)

const ksymMax = 300000

var (
	ksymLoadOnce sync.Once
	ksymTable    symbols
	ksymUnknown  = &symbol{Name: "[unknown]"}
)

const (
	// KsymStackMinDepth is the minimum supported kernel stack depth.
	KsymStackMinDepth = 16
	// KsymStackMaxDepth is the maximum supported kernel stack depth.
	KsymStackMaxDepth = 127
	// KsymPerfStackDepth is the default perf kernel stack depth.
	KsymPerfStackDepth = 20
)

// KsymbolRange is the half-open text range occupied by a kernel symbol.
type KsymbolRange struct {
	Start uint64
	End   uint64
}

// KsymbolProfile contains requested live kernel text symbols. Missing symbols
// are omitted so callers can select among version-specific alternatives.
type KsymbolProfile struct {
	Addresses map[string]uint64
	Ranges    map[string]KsymbolRange
}

// KsymStackBytes resolves kernel stack addresses into byte frames (innermost first).
func KsymStackBytes(kstack []uint64, kstackSize int) [][]byte {
	return dumpKernelBackTrace(kstack, kstackSize, outTypeBytes, false).bytes
}

// KsymStackStrs resolves kernel stack addresses into string frames (innermost first).
func KsymStackStrs(kstack []uint64, kstackSize int) []string {
	return dumpKernelBackTrace(kstack, kstackSize, outTypeString, false).strings
}

// KsymStackBytesReversed resolves kernel stack addresses into byte frames (outermost first).
func KsymStackBytesReversed(kstack []uint64, kstackSize int) [][]byte {
	return dumpKernelBackTrace(kstack, kstackSize, outTypeBytes, true).bytes
}

// KsymStackStrsReversed resolves kernel stack addresses into string frames (outermost first).
func KsymStackStrsReversed(kstack []uint64, kstackSize int) []string {
	return dumpKernelBackTrace(kstack, kstackSize, outTypeString, true).strings
}

// KsymbolSearchAddr returns the address of a kernel symbol by name.
func KsymbolSearchAddr(name string) (uint64, error) {
	ensureKsymsLoaded()
	for _, s := range ksymTable {
		if s.Name == name {
			return s.Addr, nil
		}
	}
	return 0, fmt.Errorf("symbol %q not found in %q", name, procfs.Path("kallsyms"))
}

// KsymbolSearchProfile resolves address-only and range targets in one scan.
// Module symbols and non-text symbols do not delimit kernel text ranges.
func KsymbolSearchProfile(
	addressNames []string,
	rangeNames []string,
) (KsymbolProfile, error) {
	targets := make(map[string]bool, len(addressNames)+len(rangeNames))
	for _, name := range addressNames {
		targets[name] = false
	}
	for _, name := range rangeNames {
		targets[name] = true
	}

	path := procfs.Path("kallsyms")
	file, err := os.Open(path)
	if err != nil {
		return KsymbolProfile{}, fmt.Errorf("open %q: %w", path, err)
	}
	defer file.Close()

	profile := KsymbolProfile{
		Addresses: make(map[string]uint64, len(addressNames)),
		Ranges:    make(map[string]KsymbolRange, len(rangeNames)),
	}
	var openRanges []string
	var openRangeStart uint64
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		entry, err := parseKallsymsEntry(scanner.Text())
		if err != nil {
			return KsymbolProfile{}, fmt.Errorf(
				"malformed kallsyms line %d: %w", lineNumber, err)
		}
		if entry.module != "" ||
			(entry.symbolType != 'T' && entry.symbolType != 't' &&
				entry.symbolType != 'W' && entry.symbolType != 'w') {
			continue
		}

		if len(openRanges) != 0 && entry.addr > openRangeStart {
			for _, name := range openRanges {
				value := profile.Ranges[name]
				value.End = entry.addr
				profile.Ranges[name] = value
			}
			openRanges = nil
		}

		needsRange, requested := targets[entry.name]
		if !requested {
			continue
		}
		if entry.addr == 0 {
			return KsymbolProfile{}, fmt.Errorf(
				"kernel text symbol %q has zero address in %q",
				entry.name, path)
		}
		if !needsRange {
			address, found := profile.Addresses[entry.name]
			if found && address != entry.addr {
				return KsymbolProfile{}, fmt.Errorf(
					"kernel text symbol %q is ambiguous in %q",
					entry.name, path)
			}
			profile.Addresses[entry.name] = entry.addr
			continue
		}

		value, found := profile.Ranges[entry.name]
		if found {
			if value.Start != entry.addr {
				return KsymbolProfile{}, fmt.Errorf(
					"kernel text symbol %q is ambiguous in %q",
					entry.name, path)
			}
			continue
		}
		profile.Ranges[entry.name] = KsymbolRange{Start: entry.addr}
		if len(openRanges) == 0 {
			openRangeStart = entry.addr
		}
		openRanges = append(openRanges, entry.name)
	}
	if err := scanner.Err(); err != nil {
		return KsymbolProfile{}, fmt.Errorf("scan %q: %w", path, err)
	}
	for name, value := range profile.Ranges {
		if value.End == 0 || value.Start >= value.End {
			return KsymbolProfile{}, fmt.Errorf(
				"kernel text symbol %q has no executable upper bound in %q",
				name,
				path,
			)
		}
	}
	return profile, nil
}

// dumpKernelBackTrace resolves kernel addresses into stackFrames up to maxDepth frames.
// reversed=true returns outermost frame first (original BPF order reversed);
// reversed=false returns innermost frame first (top-of-stack first).
func dumpKernelBackTrace(stack []uint64, maxDepth int, out outType, reversed bool) stackFrames {
	if len(stack) > maxDepth {
		stack = stack[:maxDepth]
	}
	ensureKsymsLoaded()
	frames := resolveStack(stack, func(addr uint64) string {
		sym := ksymTable.floorSym(addr)
		if sym == nil {
			return failFrame("ksym-not-found", "")
		}
		return fmt.Sprintf("%s/+%d %s", sym.Name, addr-sym.Addr, sym.Module)
	}, out)

	if reversed {
		if out == outTypeBytes {
			slices.Reverse(frames.bytes)
		} else {
			slices.Reverse(frames.strings)
		}
	}
	return frames
}

// ensureKsymsLoaded loads kallsyms exactly once; on failure it logs a warning and leaves ksymTable empty.
func ensureKsymsLoaded() {
	ksymLoadOnce.Do(func() {
		tbl, err := scanKallsyms(procfs.Path("kallsyms"), ksymMax)
		if err != nil {
			log.Warnf("symbol: failed to load kallsyms: %v", err)
			return
		}
		ksymTable = append(symbols{ksymUnknown}, tbl...)
		ksymTable[1:].sort()
	})
}
