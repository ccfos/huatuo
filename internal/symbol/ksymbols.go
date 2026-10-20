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
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
)

const ksymMax = 300000

const ksymStackRetryInterval = time.Second

type ksymLoadFailure struct {
	err     error
	retryAt time.Time
}

var (
	ksymLoadMu      sync.Mutex
	ksymTable       atomic.Pointer[symbols]
	ksymLastFailure atomic.Pointer[ksymLoadFailure]
	ksymUnknown     = &symbol{Name: "[unknown]"}
)

const (
	// KsymStackMinDepth is the minimum supported kernel stack depth.
	KsymStackMinDepth = 16
	// KsymStackMaxDepth is the maximum supported kernel stack depth.
	KsymStackMaxDepth = 127
	// KsymPerfStackDepth is the default perf kernel stack depth.
	KsymPerfStackDepth = 20
)

// KsymbolRange contains an address and the available kernel symbol range end.
type KsymbolRange struct {
	Start uint64
	End   uint64
}

// KsymbolLookupMode selects the requested kernel symbol information.
type KsymbolLookupMode uint8

const (
	// KsymbolLookupDefault preserves ordinary address lookup behavior.
	KsymbolLookupDefault KsymbolLookupMode = iota
	// KsymbolLookupData reads only the requested data symbol addresses.
	KsymbolLookupData
	// KsymbolLookupRange additionally resolves kernel symbol range ends.
	KsymbolLookupRange
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

// KsymbolSearchAddr returns addresses for the requested symbols.
// The default mode retains ordinary lookups. Data and range lookups read the
// requested symbols without adding them to the stack symbol table.
// Missing names are omitted; an unavailable range end remains zero.
func KsymbolSearchAddr(names []string, mode KsymbolLookupMode) (map[string]KsymbolRange, error) {
	result := make(map[string]KsymbolRange, len(names))
	switch mode {
	default:
		table, err := ensureKsymsLoaded(false)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			for _, s := range table {
				if s.Name == name {
					result[name] = KsymbolRange{Start: s.Addr}
					break
				}
			}
		}
	case KsymbolLookupData:
		path := procfs.Path("kallsyms")
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %q: %w", path, err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			words := strings.Fields(scanner.Text())
			name := words[2]
			if !slices.Contains(names, name) {
				continue
			}
			if _, exists := result[name]; exists {
				continue
			}
			addr, err := strconv.ParseUint(words[0], 16, 64)
			if err != nil {
				continue
			}
			result[name] = KsymbolRange{Start: addr}
		}
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("scan %q: %w", path, err)
		}
	case KsymbolLookupRange:
		path := procfs.Path("kallsyms")
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %q: %w", path, err)
		}
		defer file.Close()

		// Complete one range before scanning for the next requested name.
		for _, name := range names {
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return nil, fmt.Errorf("rewind %q: %w", path, err)
			}
			var value KsymbolRange
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				// Kernel records have three fields, plus [module] for modules.
				words := strings.Fields(scanner.Text())
				if value.Start == 0 && words[2] != name {
					continue
				}
				addr, err := strconv.ParseUint(words[0], 16, 64)
				if err != nil {
					continue
				}
				if value.Start == 0 {
					value.Start = addr
					result[name] = value
					if len(words) > 3 {
						break
					}
				} else if len(words) == 3 && addr > value.Start {
					// Match kallsyms: the next distinct address bounds a symbol.
					value.End = addr
					result[name] = value
					break
				}
			}
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("scan %q: %w", path, err)
			}
		}
	}
	return result, nil
}

// dumpKernelBackTrace resolves kernel addresses into stackFrames up to maxDepth frames.
// reversed=true returns outermost frame first (original BPF order reversed);
// reversed=false returns innermost frame first (top-of-stack first).
func dumpKernelBackTrace(stack []uint64, maxDepth int, out outType, reversed bool) stackFrames {
	if len(stack) > maxDepth {
		stack = stack[:maxDepth]
	}
	// Keep stack output best-effort: a failed load resolves to unknown.
	table, _ := ensureKsymsLoaded(true)
	frames := resolveStack(stack, func(addr uint64) string {
		sym := table.floorSym(addr)
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

// Publish only a complete immutable table. Failed loads remain retryable;
// successful readers need no lock and never observe a partially sorted table.
func ensureKsymsLoaded(isBestEffort bool) (symbols, error) {
	if table := ksymTable.Load(); table != nil {
		return *table, nil
	}
	// Stack events reuse a recent failure without locking or scanning again.
	// Explicit address queries must remain immediately retryable.
	if isBestEffort {
		if failure := ksymLastFailure.Load(); failure != nil && time.Now().Before(failure.retryAt) {
			return nil, failure.err
		}
	}
	ksymLoadMu.Lock()
	defer ksymLoadMu.Unlock()
	if table := ksymTable.Load(); table != nil {
		return *table, nil
	}
	// Another loader may have failed while this stack reader waited.
	if isBestEffort {
		if failure := ksymLastFailure.Load(); failure != nil && time.Now().Before(failure.retryAt) {
			return nil, failure.err
		}
	}
	table, err := scanKallsyms(procfs.Path("kallsyms"), ksymMax)
	if err != nil {
		ksymLastFailure.Store(&ksymLoadFailure{
			err:     err,
			retryAt: time.Now().Add(ksymStackRetryInterval),
		})
		log.Warnf("symbol: failed to load kallsyms: %v", err)
		return nil, err
	}
	// floorSym selects the last alias. Reverse ties so address lookup
	// preserves kallsyms' first name while name lookup retains all aliases.
	slices.Reverse(table)
	sort.SliceStable(table, func(i, j int) bool { return table[i].Addr < table[j].Addr })
	table = append(symbols{ksymUnknown}, table...)
	ksymTable.Store(&table)
	return table, nil
}
