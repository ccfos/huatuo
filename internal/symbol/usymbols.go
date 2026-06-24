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
	"debug/elf"
	"fmt"
	"path/filepath"
	"slices"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/profiler/procutil"
	"huatuo-bamai/internal/utils/fileutil"
)

type elfCache struct {
	secs sections
	syms symbols
}

type libCache struct {
	syms symbols
}

type cacheKey struct {
	inode    uint64 //nolint:unused // used implicitly via map key equality; never accessed by name
	mountKey string
}

// UsymResolver resolves user-space stack addresses to symbol names across pids.
type UsymResolver struct {
	exeCache  map[cacheKey]*elfCache // inode+xfs → elfcache
	exeKeys   map[uint32]cacheKey    // pid → cachekey
	libcaches map[cacheKey]*libCache // inode+xfs → libcache
	libKeys   map[string]cacheKey    // libpath → cachekey
	procmaps  map[uint32]sections
}

// NewUsymResolver creates a UsymResolver with shared caches across pids.
func NewUsymResolver() *UsymResolver {
	return &UsymResolver{
		exeCache:  make(map[cacheKey]*elfCache),
		exeKeys:   make(map[uint32]cacheKey),
		libcaches: make(map[cacheKey]*libCache),
		libKeys:   make(map[string]cacheKey),
		procmaps:  make(map[uint32]sections),
	}
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
	frames := resolveStack(stack[:limit], func(addr uint64) string {
		return r.resolveAddr(pid, addr)
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

func (r *UsymResolver) resolveAddr(pid uint32, addr uint64) string {
	cache, err := r.loadElfCaches(pid)
	if err != nil {
		return failFrame("elf-load-fail", "")
	}

	m := cache.secs.find(addr)
	if m != nil {
		if sym := cache.syms.resolve(addr); sym != "" {
			return sym
		}
		return failFrame("elf-no-sym", "")
	}

	if err = r.loadProcMaps(pid); err != nil {
		return failFrame("procmap-fail", "")
	}
	m = r.procmaps[pid].find(addr)
	if m == nil {
		return failFrame("proc-unmapped", "")
	}
	if !isLibPath(m.Pathname) {
		return failFrame("non-lib", m.Pathname)
	}

	rootDir := procfs.Path(fmt.Sprintf("%d/root", pid))
	libPath := filepath.Join(rootDir, m.Pathname)

	libCache, err := r.loadLibCache(pid, libPath)
	if err != nil {
		return failFrame("lib-load-fail", m.Pathname)
	}
	baseAddr, ok := r.procmaps[pid].findBaseAddr(m.Pathname)
	if !ok {
		return failFrame("no-baseaddr", m.Pathname)
	}
	if sym := libCache.syms.resolve(addr - baseAddr); sym != "" {
		return sym
	}
	return failFrame("lib-no-sym", m.Pathname)
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
		secs: secs,
		syms: elfSymbols(f),
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
	defer f.Close()

	cache = &libCache{syms: elfSymbols(f)}
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
