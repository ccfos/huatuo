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
	"fmt"
	"slices"
	"sync"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
)

const ksymMax = 300000

// ksymFrameCacheCap bounds the resolved-frame cache; kernel stack addresses
// repeat heavily across events (the same hot functions), so a small cache
// removes most floorSym binary searches and frame-string allocations.
const ksymFrameCacheCap = 8192

var (
	ksymLoadOnce sync.Once
	ksymTable    symbols
	ksymUnknown  = &symbol{Name: "[unknown]"}

	ksymFrameMu    sync.Mutex
	ksymFrameCache = make(map[uint64]string, ksymFrameCacheCap)
)

// ksymFrameString resolves addr through ksymTable and caches the formatted
// frame. The cache is cleared wholesale when it fills; this keeps memory
// bounded without per-entry eviction.
func ksymFrameString(addr uint64) string {
	ksymFrameMu.Lock()
	frame, ok := ksymFrameCache[addr]
	if !ok {
		ensureKsymsLoaded()
		sym := ksymTable.floorSym(addr)
		if sym == nil {
			frame = failFrame("ksym-not-found", "")
		} else {
			frame = fmt.Sprintf("%s/+%d %s", sym.Name, addr-sym.Addr, sym.Module)
		}
		if len(ksymFrameCache) >= ksymFrameCacheCap {
			clear(ksymFrameCache)
		}
		ksymFrameCache[addr] = frame
	}
	ksymFrameMu.Unlock()
	return frame
}

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
	for _, s := range ksymTable {
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
	frames := resolveStack(stack, ksymFrameString, out)

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
