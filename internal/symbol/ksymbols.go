// Copyright 2025 The HuaTuo Authors
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
	"fmt"
	"slices"
	"sync"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/procfs"
)

const ksymMax = 300000

var (
	ksymOnce      sync.Once
	kernelSymbols symbols
	defaultKsym   = &symbol{Name: "[unknown]"}
)

const (
	// KsymStackMinDepth is the minimum supported kernel stack depth.
	KsymStackMinDepth = 16
	// KsymStackMaxDepth is the maximum supported kernel stack depth.
	KsymStackMaxDepth = 127
	// KsymPerfStackDepth is the default perf kernel stack depth.
	KsymPerfStackDepth = 20
)

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
	for _, s := range kernelSymbols {
		if s.Name == name {
			return s.Addr, nil
		}
	}
	return 0, fmt.Errorf("symbol %q not found in %q", name, procfs.Path("kallsyms"))
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
		sym := kernelSymbols.floorSym(addr)
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

// ensureKsymsLoaded loads kallsyms exactly once; on failure it logs a warning and leaves kernelSymbols empty.
func ensureKsymsLoaded() {
	ksymOnce.Do(func() {
		tbl, err := scanKallsyms(procfs.Path("kallsyms"), ksymMax)
		if err != nil {
			log.Warnf("symbol: failed to load kallsyms: %v", err)
			return
		}
		kernelSymbols = append(symbols{defaultKsym}, tbl...)
		kernelSymbols[1:].sort()
	})
}
