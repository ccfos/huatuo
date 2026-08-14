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
	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/utils/fileutil"
)

type elfCache struct {
	secs     sections
	syms     symbols // used by synthetic tests; real caches resolve PCs lazily
	state    *elfSymbolParseState
	path     string
	module   string
	typ      elf.Type
	paths    map[uint32]string
	modules  map[uint32]string
	resolved map[uint64]string
}

type libCache struct {
	syms     symbols // used by synthetic tests; real caches resolve PCs lazily
	state    *elfSymbolParseState
	resolved map[uint64]string
}

type cacheKey struct {
	inode    uint64 //nolint:unused // used implicitly via map key equality; never accessed by name
	mountKey string
}

// UsymResolver resolves user-space stack addresses to symbol names across pids.
type UsymResolver struct {
	exeCache        map[cacheKey]*elfCache // inode+xfs → elfcache
	exeKeys         map[uint32]cacheKey    // pid → cachekey
	libcaches       map[cacheKey]*libCache // inode+xfs → libcache
	libKeys         map[string]cacheKey    // libpath → cachekey
	procmaps        map[uint32]sections
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
		exeCache:        make(map[cacheKey]*elfCache),
		exeKeys:         make(map[uint32]cacheKey),
		libcaches:       make(map[cacheKey]*libCache),
		libKeys:         make(map[string]cacheKey),
		procmaps:        make(map[uint32]sections),
		elfSymbolLimits: DefaultELFSymbolLimits(),
	}
	for _, option := range options {
		option(r)
	}
	return r
}

func (r *UsymResolver) resolveELFPCs(path string, fallback symbols, resolved map[uint64]string, pcs []uint64) error {
	return r.resolveELFPCsWithState(path, fallback, resolved, pcs, nil)
}

func (r *UsymResolver) resolveELFPCsWithState(path string, fallback symbols, resolved map[uint64]string, pcs []uint64, state *elfSymbolParseState) error {
	unresolved := make([]uint64, 0, len(pcs))
	seen := make(map[uint64]struct{}, len(pcs))
	for _, pc := range pcs {
		if _, ok := resolved[pc]; !ok {
			if _, duplicate := seen[pc]; duplicate {
				continue
			}
			seen[pc] = struct{}{}
			unresolved = append(unresolved, pc)
		}
	}
	if len(unresolved) == 0 {
		return nil
	}
	if path == "" {
		for _, pc := range unresolved {
			resolved[pc] = fallback.resolve(pc)
		}
		return nil
	}

	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("elf.Open %q: %w", path, err)
	}
	defer f.Close()
	if state == nil {
		state = newELFSymbolParseState(r.elfSymbolLimits)
	}
	syms, err := elfSymbolsForPCsWithState(f, unresolved, state)
	if err != nil {
		log.Debugf("symbol: parse ELF PCs for %q: %v", path, err)
	}
	for _, pc := range unresolved {
		name := syms.resolve(pc)
		if name == "" {
			name = fallback.resolve(pc)
		}
		resolved[pc] = name
	}
	return err
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
	syms     symbols
	state    *elfSymbolParseState
	resolved map[uint64]string
	pcs      []uint64
	indices  []int
	failures []string
}

type elfGroupKey struct {
	path     string
	module   string
	loadBias uint64
}

func (r *UsymResolver) resolveAddrs(pid uint32, addrs []uint64) []string {
	result := slices.Repeat([]string{failFrame("elf-load-fail", "")}, len(addrs))
	cache, err := r.loadElfCaches(pid)
	if err != nil {
		return result
	}

	groups := make(map[elfGroupKey]*pendingELFPCs)
	for index, addr := range addrs {
		module := cache.module
		if cache.modules != nil {
			module = cache.modules[pid]
		}
		path := cache.path
		if cache.paths != nil {
			path = cache.paths[pid]
		}
		if cache.typ == elf.ET_DYN && module != "" {
			if err = r.loadProcMaps(pid); err == nil {
				if m := r.procmaps[pid].find(addr); m != nil && m.Pathname == module {
					baseAddr := uint64(m.StartAddr) - uint64(m.Offset)
					if cache.resolved == nil {
						cache.resolved = make(map[uint64]string)
					}
					groupKey := elfGroupKey{path: path, module: module, loadBias: baseAddr}
					group := groups[groupKey]
					if group == nil {
						group = &pendingELFPCs{path: path, syms: cache.syms, state: cache.state, resolved: cache.resolved}
						groups[groupKey] = group
					}
					group.pcs = append(group.pcs, addr-baseAddr)
					group.indices = append(group.indices, index)
					group.failures = append(group.failures, failFrame("elf-no-sym", ""))
					continue
				}
			}
		}
		if cache.secs.find(addr) != nil {
			if cache.resolved == nil {
				cache.resolved = make(map[uint64]string)
			}
			groupKey := elfGroupKey{path: path, module: module}
			group := groups[groupKey]
			if group == nil {
				group = &pendingELFPCs{path: path, syms: cache.syms, state: cache.state, resolved: cache.resolved}
				groups[groupKey] = group
			}
			group.pcs = append(group.pcs, addr)
			group.indices = append(group.indices, index)
			group.failures = append(group.failures, failFrame("elf-no-sym", ""))
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
		if libCache.resolved == nil {
			libCache.resolved = make(map[uint64]string)
		}
		groupKey := elfGroupKey{path: libPath, module: m.Pathname, loadBias: baseAddr}
		group := groups[groupKey]
		if group == nil {
			group = &pendingELFPCs{path: libPath, syms: libCache.syms, state: libCache.state, resolved: libCache.resolved}
			groups[groupKey] = group
		}
		group.pcs = append(group.pcs, addr-baseAddr)
		group.indices = append(group.indices, index)
		group.failures = append(group.failures, failFrame("lib-no-sym", m.Pathname))
	}

	for _, group := range groups {
		if err := r.resolveELFPCsWithState(group.path, group.syms, group.resolved, group.pcs, group.state); err != nil {
			log.Debugf("symbol: resolve ELF PCs for %q: %v", group.path, err)
		}
		for offset, pc := range group.pcs {
			name := group.resolved[pc]
			if name == "" {
				name = group.failures[offset]
			}
			result[group.indices[offset]] = name
		}
	}
	return result
}

func (r *UsymResolver) loadElfCaches(pid uint32) (*elfCache, error) {
	if key, ok := r.exeKeys[pid]; ok {
		if cache, ok := r.exeCache[key]; ok {
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
		if cache.paths == nil {
			cache.paths = make(map[uint32]string)
		}
		if cache.modules == nil {
			cache.modules = make(map[uint32]string)
		}
		cache.paths[pid] = path
		cache.modules[pid] = strings.TrimPrefix(path, procfs.Path(fmt.Sprintf("%d/root", pid)))
		r.exeKeys[pid] = key
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

	cache = &elfCache{
		secs:     secs,
		path:     path,
		module:   strings.TrimPrefix(path, procfs.Path(fmt.Sprintf("%d/root", pid))),
		typ:      f.Type,
		paths:    map[uint32]string{pid: path},
		modules:  map[uint32]string{pid: strings.TrimPrefix(path, procfs.Path(fmt.Sprintf("%d/root", pid)))},
		resolved: make(map[uint64]string),
		state:    newELFSymbolParseState(r.elfSymbolLimits),
	}
	r.exeCache[key] = cache
	r.exeKeys[pid] = key
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

func (r *UsymResolver) loadLibCache(pid uint32, libPath string) (*libCache, error) {
	if key, ok := r.libKeys[libPath]; ok {
		if cache, ok := r.libcaches[key]; ok {
			return cache, nil
		}
	}

	key, err := r.libCacheKey(pid, libPath)
	if err != nil {
		return nil, err
	}

	cache, ok := r.libcaches[key]
	if ok {
		r.libKeys[libPath] = key
		return cache, nil
	}

	f, err := elf.Open(libPath)
	if err != nil {
		return nil, fmt.Errorf("elf.Open %q: %w", libPath, err)
	}
	_ = f.Close()

	cache = &libCache{
		resolved: make(map[uint64]string),
		state:    newELFSymbolParseState(r.elfSymbolLimits),
	}
	r.libcaches[key] = cache
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

	inContainer, err := procutil.IsProcessInContainer(int(pid))
	if err != nil {
		return "", err
	}
	if !inContainer {
		return matchXfsMount(hostPath, mounts)
	}

	if key, ok := r.exeKeys[pid]; ok {
		return key.mountKey, nil
	}
	lowerDir, err := lowerDirFromMountInfo(pid)
	if err != nil {
		return "", err
	}
	return matchXfsMount(lowerDir, mounts)
}
