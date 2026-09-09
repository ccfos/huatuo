// Copyright 2025-2026 The HuaTuo Authors
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
	"debug/elf"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/process"
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/utils/fileutil"
)

type executableCache struct {
	sections sections
	symbols  elfSymbolCache
	typ      elf.Type
}

type elfSymbolCache struct {
	state        *elfSymbolParseState
	namesByELFPC map[uint64]string
}

type processELF struct {
	cacheKey cacheKey
	path     string
}

type cacheKey struct {
	inode    uint64 //nolint:unused // used implicitly via map key equality; never accessed by name
	mountKey string
}

// UsymResolver resolves user-space stack addresses to symbol names across pids.
// It is not safe for concurrent use.
type UsymResolver struct {
	exeCache        map[cacheKey]*executableCache
	processes       map[uint32]processELF
	libCaches       map[cacheKey]*elfSymbolCache
	libKeys         map[string]cacheKey // libpath → cachekey
	procmaps        map[uint32]sections
	names           map[string]string
	elfSymbolLimits ELFSymbolLimits
}

// UsymResolverOption configures a UsymResolver.
type UsymResolverOption func(*UsymResolver)

// WithELFSymbolLimits configures per-ELF symbol parsing limits.
func WithELFSymbolLimits(limits ELFSymbolLimits) UsymResolverOption {
	return func(r *UsymResolver) {
		r.elfSymbolLimits = limits
	}
}

// NewUsymResolver creates a UsymResolver with shared caches across pids.
func NewUsymResolver(options ...UsymResolverOption) *UsymResolver {
	r := &UsymResolver{
		exeCache:        make(map[cacheKey]*executableCache),
		processes:       make(map[uint32]processELF),
		libCaches:       make(map[cacheKey]*elfSymbolCache),
		libKeys:         make(map[string]cacheKey),
		procmaps:        make(map[uint32]sections),
		names:           make(map[string]string),
		elfSymbolLimits: DefaultELFSymbolLimits(),
	}
	for _, option := range options {
		option(r)
	}
	return r
}

// UsymStackBytes resolves user-space stack addresses into byte frames (innermost first).
func (r *UsymResolver) UsymStackBytes(pid uint32, ustack []uint64, ustackSize int) [][]byte {
	return r.resolveUserStack(pid, ustack, ustackSize, outTypeBytes, false).bytes
}

// UsymStackStrs resolves user-space stack addresses into string frames (innermost first).
func (r *UsymResolver) UsymStackStrs(pid uint32, ustack []uint64, ustackSize int) []string {
	return r.resolveUserStack(pid, ustack, ustackSize, outTypeString, false).strings
}

// UsymStackBytesReversed resolves user-space stack addresses into byte frames (outermost first).
func (r *UsymResolver) UsymStackBytesReversed(pid uint32, ustack []uint64, ustackSize int) [][]byte {
	return r.resolveUserStack(pid, ustack, ustackSize, outTypeBytes, true).bytes
}

// UsymStackStrsReversed resolves user-space stack addresses into string frames (outermost first).
func (r *UsymResolver) UsymStackStrsReversed(pid uint32, ustack []uint64, ustackSize int) []string {
	return r.resolveUserStack(pid, ustack, ustackSize, outTypeString, true).strings
}

func (r *UsymResolver) resolveUserStack(pid uint32, stack []uint64, stackSize int, out outType, reversed bool) stackFrames {
	limit := min(stackSize, len(stack))
	valid := limit
	for index, addr := range stack[:limit] {
		if addr == 0 {
			valid = index
			break
		}
	}
	names := r.resolveAddrs(pid, stack[:valid])
	frames := stackFrames{}
	if out == outTypeBytes {
		frames.bytes = make([][]byte, 0, len(names))
		for _, name := range names {
			frames.bytes = append(frames.bytes, []byte(name))
		}
	} else {
		frames.strings = names
	}

	if reversed {
		if out == outTypeBytes {
			slices.Reverse(frames.bytes)
		} else {
			slices.Reverse(frames.strings)
		}
	}
	return frames
}

func (r *UsymResolver) resolveAddr(pid uint32, addr uint64) string {
	return r.resolveAddrs(pid, []uint64{addr})[0]
}

type pendingELFPCs struct {
	path     string
	cache    *elfSymbolCache
	pcs      []uint64
	indices  []int
	failures []string
}

func (r *UsymResolver) resolveAddrs(pid uint32, addrs []uint64) []string {
	result := slices.Repeat([]string{failFrame("elf-load-fail", "")}, len(addrs))
	cache, err := r.loadElfCaches(pid)
	if err != nil {
		return result
	}

	groups := make(map[string]*pendingELFPCs)
	for index, addr := range addrs {
		path := r.processes[pid].path
		module := strings.TrimPrefix(path, procfs.Path(fmt.Sprintf("%d/root", pid)))
		if cache.typ == elf.ET_DYN && module != "" {
			if err = r.loadProcMaps(pid); err == nil {
				if m := r.procmaps[pid].find(addr); m != nil && m.Pathname == module {
					baseAddr := uint64(m.StartAddr) - uint64(m.Offset)
					addPendingELFPC(groups, path, &cache.symbols, addr-baseAddr, index, failFrame("elf-no-sym", ""))
					continue
				}
			}
		}
		if cache.sections.find(addr) != nil {
			addPendingELFPC(groups, path, &cache.symbols, addr, index, failFrame("elf-no-sym", ""))
			continue
		}

		if err = r.loadProcMaps(pid); err != nil {
			result[index] = failFrame("procmap-fail", "")
			continue
		}
		m := r.procmaps[pid].find(addr)
		if m == nil {
			result[index] = failFrame("proc-unmapped", "")
			continue
		}
		if !isLibPath(m.Pathname) {
			result[index] = failFrame("non-lib", m.Pathname)
			continue
		}

		rootDir := procfs.Path(fmt.Sprintf("%d/root", pid))
		libPath := filepath.Join(rootDir, m.Pathname)

		libCache, loadErr := r.loadLibCache(pid, libPath)
		if loadErr != nil {
			result[index] = failFrame("lib-load-fail", m.Pathname)
			continue
		}
		baseAddr := uint64(m.StartAddr) - uint64(m.Offset)
		addPendingELFPC(groups, libPath, libCache, addr-baseAddr, index, failFrame("lib-no-sym", m.Pathname))
	}

	for _, group := range groups {
		names, err := r.resolveELFPCs(group.path, group.cache, group.pcs)
		if err != nil {
			log.Debugf("symbol: resolve ELF PCs for %q: %v", group.path, err)
		}
		r.fillELFFrames(result, group, names)
	}
	return result
}

func addPendingELFPC(groups map[string]*pendingELFPCs, path string, cache *elfSymbolCache, pc uint64, index int, failure string) {
	group := groups[path]
	if group == nil {
		group = &pendingELFPCs{path: path, cache: cache}
		groups[path] = group
	}
	group.pcs = append(group.pcs, pc)
	group.indices = append(group.indices, index)
	group.failures = append(group.failures, failure)
}

func (r *UsymResolver) fillELFFrames(result []string, group *pendingELFPCs, names map[uint64]string) {
	for offset, pc := range group.pcs {
		name := names[pc]
		if name == "" {
			name = group.failures[offset]
		} else {
			name = r.displayName(name)
		}
		result[group.indices[offset]] = name
	}
}

func (r *UsymResolver) displayName(name string) string {
	if display, ok := r.names[name]; ok {
		return display
	}
	display := demangleSymbolName(name)
	r.names[name] = display
	return display
}

func (r *UsymResolver) resolveELFPCs(path string, cache *elfSymbolCache, pcs []uint64) (map[uint64]string, error) {
	result := make(map[uint64]string, len(pcs))
	for _, pc := range pcs {
		if name := cache.namesByELFPC[pc]; name != "" {
			result[pc] = name
		}
	}
	unresolved := unresolvedELFPCs(pcs, result)
	if len(unresolved) == 0 {
		return result, nil
	}
	f, err := elf.Open(path)
	if err != nil {
		return result, fmt.Errorf("open ELF %q: %w", path, err)
	}
	defer f.Close()
	if cache.state == nil {
		cache.state = newELFSymbolParseState(r.elfSymbolLimits)
	}
	syms, err := elfSymbolsForPCsWithState(f, unresolved, cache.state)
	if err != nil {
		log.Debugf("symbol: parse ELF PCs for %q: %v", path, err)
	}
	if cache.namesByELFPC == nil {
		cache.namesByELFPC = make(map[uint64]string)
	}
	for _, sym := range syms {
		result[sym.Addr] = sym.Name
		// Full caches still return this batch's results, but retain neither
		// misses nor additional entries beyond the per-ELF symbol budget.
		if sym.Name != "" && uint64(len(cache.namesByELFPC)) < r.elfSymbolLimits.MaxSymbolCount {
			cache.namesByELFPC[sym.Addr] = sym.Name
		}
	}
	return result, err
}

func unresolvedELFPCs(pcs []uint64, cached map[uint64]string) []uint64 {
	result := make([]uint64, 0, len(pcs))
	seen := make(map[uint64]struct{}, len(pcs))
	for _, pc := range pcs {
		if cached[pc] != "" {
			continue
		}
		if _, ok := seen[pc]; !ok {
			seen[pc] = struct{}{}
			result = append(result, pc)
		}
	}
	return result
}

func (r *UsymResolver) loadElfCaches(pid uint32) (*executableCache, error) {
	if process, ok := r.processes[pid]; ok {
		if cache, ok := r.exeCache[process.cacheKey]; ok {
			return cache, nil
		}
	}

	path, err := r.exePath(pid)
	if err != nil {
		return nil, err
	}

	key, err := r.exeCacheKey(pid, path)
	if err != nil {
		return nil, err
	}
	cache, ok := r.exeCache[key]
	if ok {
		r.processes[pid] = processELF{cacheKey: key, path: path}
		return cache, nil
	}

	f, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("elf.Open %q: %w", path, err)
	}
	defer f.Close()

	secs := make(sections, 0, len(f.Sections))
	for _, s := range f.Sections {
		secs = append(secs, &procfs.ProcMap{
			StartAddr: uintptr(s.Addr),
			EndAddr:   uintptr(s.Addr + s.Size),
			Pathname:  s.Name,
		})
	}
	secs.sort()

	cache = &executableCache{
		sections: secs,
		typ:      f.Type,
		symbols:  elfSymbolCache{state: newELFSymbolParseState(r.elfSymbolLimits)},
	}
	r.exeCache[key] = cache
	r.processes[pid] = processELF{cacheKey: key, path: path}
	return cache, nil
}

func (r *UsymResolver) loadProcMaps(pid uint32) error {
	_, ok := r.procmaps[pid]
	if ok {
		return nil
	}

	maps, err := parseMaps(pid)
	if err != nil {
		return err
	}
	r.procmaps[pid] = maps
	return nil
}

func (r *UsymResolver) loadLibCache(pid uint32, libPath string) (*elfSymbolCache, error) {
	if key, ok := r.libKeys[libPath]; ok {
		if cache, ok := r.libCaches[key]; ok {
			return cache, nil
		}
	}

	key, err := r.libCacheKey(pid, libPath)
	if err != nil {
		return nil, err
	}

	cache, ok := r.libCaches[key]
	if ok {
		r.libKeys[libPath] = key
		return cache, nil
	}

	f, err := elf.Open(libPath)
	if err != nil {
		return nil, fmt.Errorf("elf.Open %q: %w", libPath, err)
	}
	_ = f.Close()

	cache = &elfSymbolCache{state: newELFSymbolParseState(r.elfSymbolLimits)}
	r.libCaches[key] = cache
	r.libKeys[libPath] = key
	return cache, nil
}

func (r *UsymResolver) exePath(pid uint32) (string, error) {
	proc, err := procfs.NewProc(int(pid))
	if err != nil {
		return "", fmt.Errorf("procfs.NewProc %d: %w", pid, err)
	}
	bin, err := proc.Executable()
	if err != nil {
		return "", fmt.Errorf("proc.Executable %d: %w", pid, err)
	}
	rootDir := procfs.Path(fmt.Sprintf("%d/root", pid))
	return filepath.Join(rootDir, bin), nil
}

func (r *UsymResolver) exeCacheKey(pid uint32, path string) (cacheKey, error) {
	inode, err := fileutil.StatInode(path)
	if err != nil {
		return cacheKey{}, fmt.Errorf("stat %q: %w", path, err)
	}

	mountKey, err := r.mountKeyForPID(pid, path)
	if err != nil {
		return cacheKey{}, err
	}

	return cacheKey{inode: inode, mountKey: mountKey}, nil
}

func (r *UsymResolver) libCacheKey(pid uint32, libPath string) (cacheKey, error) {
	inode, err := fileutil.StatInode(libPath)
	if err != nil {
		return cacheKey{}, fmt.Errorf("stat %q: %w", libPath, err)
	}

	mountKey, err := r.mountKeyForPID(pid, libPath)
	if err != nil {
		return cacheKey{}, err
	}

	return cacheKey{inode: inode, mountKey: mountKey}, nil
}

func (r *UsymResolver) mountKeyForPID(pid uint32, hostPath string) (string, error) {
	count, err := countXfsMounts()
	if err != nil {
		return "", err
	}
	if count < 2 {
		return "", nil
	}

	inContainer, err := process.IsInContainer(int(pid))
	if err != nil {
		return "", err
	}
	if !inContainer {
		return matchXfsMount(hostPath, mounts)
	}

	if process, ok := r.processes[pid]; ok {
		return process.cacheKey.mountKey, nil
	}
	lowerDir, err := lowerDirFromMountInfo(pid)
	if err != nil {
		return "", err
	}
	return matchXfsMount(lowerDir, mounts)
}
