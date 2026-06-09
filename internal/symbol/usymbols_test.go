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

package symbol

import (
	"debug/elf"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"huatuo-bamai/internal/procfs"
)

func copyCurrentExecutable(t *testing.T, destinationPath string) {
	t.Helper()
	sourcePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("Open(%q): %v", sourcePath, err)
	}
	defer sourceFile.Close()

	destinationFile, err := os.Create(destinationPath)
	if err != nil {
		t.Fatalf("Create(%q): %v", destinationPath, err)
	}
	defer destinationFile.Close()

	if _, err = io.Copy(destinationFile, sourceFile); err != nil {
		t.Fatalf("Copy(%q -> %q): %v", sourcePath, destinationPath, err)
	}
}

func firstFunctionSymbol(t *testing.T, elfPath string) (string, uint64) {
	t.Helper()
	elfFile, err := elf.Open(elfPath)
	if err != nil {
		t.Fatalf("elf.Open(%q): %v", elfPath, err)
	}
	defer elfFile.Close()

	for _, fetch := range []func() ([]elf.Symbol, error){elfFile.DynamicSymbols, elfFile.Symbols} {
		elfSymbols, fetchErr := fetch()
		if fetchErr != nil {
			continue
		}
		for _, entry := range elfSymbols {
			if elf.ST_TYPE(entry.Info) == elf.STT_FUNC && entry.Name != "" {
				return entry.Name, entry.Value
			}
		}
	}
	t.Fatalf("no STT_FUNC symbol in %q", elfPath)
	return "", 0
}

func setupMainElfResolverFixture(t *testing.T) (*UserResolver, uint32, string, uint64) {
	t.Helper()
	setTestXfsMounts(t, []string{"/"})
	tmpRoot := setupTempProcRoot(t)
	processID := uint32(1001)
	procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
	rootTarget := filepath.Join(tmpRoot, "container-root")

	mustMkdirAll(t, procDir)
	mustMkdirAll(t, filepath.Join(rootTarget, "usr", "bin"))
	mustSymlink(t, rootTarget, filepath.Join(procDir, "root"))

	executablePath := filepath.Join(rootTarget, "usr", "bin", "huatuo-dev")
	copyCurrentExecutable(t, executablePath)
	mustSymlink(t, "/usr/bin/huatuo-dev", filepath.Join(procDir, "exe"))

	functionName, functionAddr := firstFunctionSymbol(t, executablePath)
	return NewUserResolver(), processID, functionName, functionAddr
}

func setupLibraryResolverFixture(t *testing.T) (*UserResolver, uint32, string, uint64) {
	t.Helper()
	setTestXfsMounts(t, []string{"/"})
	tmpRoot := setupTempProcRoot(t)
	processID := uint32(1001)
	procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
	rootTarget := filepath.Join(tmpRoot, "container-root")

	mustMkdirAll(t, procDir)
	mustMkdirAll(t, filepath.Join(rootTarget, "usr", "bin"))
	mustMkdirAll(t, filepath.Join(rootTarget, "usr", "lib"))
	mustSymlink(t, rootTarget, filepath.Join(procDir, "root"))

	executablePath := filepath.Join(rootTarget, "usr", "bin", "huatuo-dev")
	copyCurrentExecutable(t, executablePath)
	mustSymlink(t, "/usr/bin/huatuo-dev", filepath.Join(procDir, "exe"))

	libraryPath := filepath.Join(rootTarget, "usr", "lib", "libhuatuo.so")
	copyCurrentExecutable(t, libraryPath)
	functionName, functionAddr := firstFunctionSymbol(t, libraryPath)

	mapStart := uint64(0x70000000)
	mapsContent := "70000000-71000000 r-xp 00000000 fd:01 1001 /usr/lib/libhuatuo.so\n"
	mustWriteFile(t, filepath.Join(procDir, "maps"), mapsContent)

	return NewUserResolver(), processID, functionName, mapStart + functionAddr
}

func TestNewUserResolver(t *testing.T) {
	resolver := NewUserResolver()
	if resolver == nil {
		t.Fatalf("NewUserResolver(): got nil resolver")
	}
	if resolver.exeCache == nil || resolver.libcaches == nil || resolver.procmaps == nil {
		t.Errorf("NewUserResolver(): caches not initialized")
	}
}

func TestUserResolverExePath(t *testing.T) {
	tests := []struct {
		name     string
		build    func(t *testing.T) (*UserResolver, uint32, string)
		wantErr  bool
		wantPath string
	}{
		{
			name: "pid-not-found",
			build: func(t *testing.T) (*UserResolver, uint32, string) {
				setupTempProcRoot(t)
				return NewUserResolver(), uint32(1001), ""
			},
			wantErr: true,
		},
		{
			name: "resolve-executable-under-root",
			build: func(t *testing.T) (*UserResolver, uint32, string) {
				tmpRoot := setupTempProcRoot(t)
				processID := uint32(1001)
				procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
				procDirRoot := filepath.Join(procDir, "root")
				mustMkdirAll(t, procDir)
				mustSymlink(t, "/usr/bin/huatuo-dev", filepath.Join(procDir, "exe"))
				return NewUserResolver(), processID, filepath.Join(procDirRoot, "/usr/bin/huatuo-dev")
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, processID, expectedPath := tt.build(t)
			gotPath, err := resolver.exePath(processID)
			if tt.wantErr {
				if err == nil {
					t.Errorf("exePath(%d): got nil error, want non-nil", processID)
				}
				return
			}
			if err != nil {
				t.Fatalf("exePath(%d): %v", processID, err)
			}
			if gotPath != expectedPath {
				t.Errorf("exePath(%d): got %q, want %q", processID, gotPath, expectedPath)
			}
		})
	}
}

func TestUserResolverLoadElfCaches(t *testing.T) {
	t.Run("repeated-load-shares-cache-entry", func(t *testing.T) {
		resolver, processID, _, _ := setupMainElfResolverFixture(t)

		firstCache, err := resolver.loadElfCaches(processID)
		if err != nil {
			t.Fatalf("loadElfCaches first: %v", err)
		}
		if firstCache == nil {
			t.Fatalf("loadElfCaches first: got nil cache")
		}

		secondCache, err := resolver.loadElfCaches(processID)
		if err != nil {
			t.Fatalf("loadElfCaches second: %v", err)
		}
		if secondCache == nil {
			t.Fatalf("loadElfCaches second: got nil cache")
		}
		if firstCache != secondCache {
			t.Errorf("loadElfCaches: expected same cache pointer for repeated loads")
		}
		if len(resolver.exeCache) != 1 {
			t.Errorf("loadElfCaches: got %d cached executables, want 1", len(resolver.exeCache))
		}
	})

	// Two pids whose overlay exe paths (/proc/<pid>/root/...) ultimately
	// resolve to the same backing file on the same xfs mount. The cacheKey
	// must be identical so the ELF is parsed only once and shared across pids.
	t.Run("shared-backing-file-across-containers-loaded-once", func(t *testing.T) {
		tmpRoot := setupTempProcRoot(t)
		setTestXfsMounts(t, []string{"/"})

		backingBin := filepath.Join(tmpRoot, "backing", "huatuo-dev")
		mustMkdirAll(t, filepath.Dir(backingBin))
		copyCurrentExecutable(t, backingBin)

		pidFirst := uint32(1001)
		pidSecond := uint32(1002)
		for _, processID := range []uint32{pidFirst, pidSecond} {
			procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
			rootTarget := filepath.Join(tmpRoot, "overlay-root-"+strconv.Itoa(int(processID)))
			mustMkdirAll(t, procDir)
			mustMkdirAll(t, filepath.Join(rootTarget, "usr", "bin"))
			// The overlay exe path is a symlink to the same backing file, so
			// syscall.Stat returns an identical (dev, inode) for both pids.
			mustSymlink(t, backingBin, filepath.Join(rootTarget, "usr", "bin", "huatuo-dev"))
			mustSymlink(t, rootTarget, filepath.Join(procDir, "root"))
			mustSymlink(t, "/usr/bin/huatuo-dev", filepath.Join(procDir, "exe"))
		}

		resolver := NewUserResolver()
		cacheFirst, err := resolver.loadElfCaches(pidFirst)
		if err != nil {
			t.Fatalf("loadElfCaches(pidFirst): %v", err)
		}
		cacheSecond, err := resolver.loadElfCaches(pidSecond)
		if err != nil {
			t.Fatalf("loadElfCaches(pidSecond): %v", err)
		}
		if cacheFirst != cacheSecond {
			t.Errorf("loadElfCaches: expected same cache pointer across pids sharing backing file")
		}
		if len(resolver.exeCache) != 1 {
			t.Errorf("loadElfCaches: got %d cache entries, want 1 (shared backing file on same xfs)", len(resolver.exeCache))
		}
	})
}

func TestUserResolverLoadProcMaps(t *testing.T) {
	setTestXfsMounts(t, []string{"/"})
	tmpRoot := setupTempProcRoot(t)
	processID := uint32(1001)
	procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
	mustMkdirAll(t, procDir)
	mapsContent := "70000000-71000000 r-xp 00000000 fd:01 1001 /usr/lib/libhuatuo.so\n" +
		"72000000-72100000 r--p 00000000 fd:01 1002 [heap]\n"
	mustWriteFile(t, filepath.Join(procDir, "maps"), mapsContent)

	resolver := NewUserResolver()
	if err := resolver.loadProcMaps(processID); err != nil {
		t.Fatalf("loadProcMaps first: %v", err)
	}
	if err := resolver.loadProcMaps(processID); err != nil {
		t.Fatalf("loadProcMaps second: %v", err)
	}

	if len(resolver.procmaps[processID]) != 2 {
		t.Errorf("loadProcMaps: got %d maps, want 2", len(resolver.procmaps[processID]))
	}
}

func TestUserResolverLoadProcMapsNotFound(t *testing.T) {
	setTestXfsMounts(t, []string{"/"})
	setupTempProcRoot(t)
	resolver := NewUserResolver()
	if err := resolver.loadProcMaps(uint32(1001)); err == nil {
		t.Errorf("loadProcMaps not-found: got nil error, want non-nil")
	}
}

func TestUserResolverLoadLibCache(t *testing.T) {
	t.Run("repeated-load-shares-cache-entry", func(t *testing.T) {
		setTestXfsMounts(t, []string{"/"})
		tmpRoot := setupTempProcRoot(t)
		libraryDir := filepath.Join(tmpRoot, "libs")
		mustMkdirAll(t, libraryDir)
		libraryPath := filepath.Join(libraryDir, "libhuatuo.so")
		copyCurrentExecutable(t, libraryPath)

		resolver := NewUserResolver()
		firstCache, err := resolver.loadLibCache(uint32(1001), libraryPath)
		if err != nil {
			t.Fatalf("loadLibCache first: %v", err)
		}
		secondCache, err := resolver.loadLibCache(uint32(1001), libraryPath)
		if err != nil {
			t.Fatalf("loadLibCache second: %v", err)
		}
		if firstCache != secondCache {
			t.Errorf("loadLibCache: expected same cache pointer for repeated loads")
		}
		if len(resolver.libcaches) != 1 {
			t.Errorf("loadLibCache: got %d caches, want 1", len(resolver.libcaches))
		}
	})

	t.Run("missing-library-returns-error", func(t *testing.T) {
		setTestXfsMounts(t, []string{"/"})
		setupTempProcRoot(t)
		resolver := NewUserResolver()
		if _, err := resolver.loadLibCache(uint32(1001), "/proc/1001/root/lib-not-found.so"); err == nil {
			t.Errorf("loadLibCache not-found: got nil error, want non-nil")
		}
	})

	// Host process whose two library paths share the same inode (via hardlink)
	// but live on different xfs mounts. The mountKey must distinguish them so
	// both libraries are cached separately instead of colliding.
	t.Run("same-inode-different-xfs-mounts-both-loaded", func(t *testing.T) {
		tmpRoot := setupTempProcRoot(t)
		processID := uint32(1001)
		setupHostProcessProcFS(t, tmpRoot, processID)

		mountPathData := filepath.Join(tmpRoot, "xfs-data")
		mountPathLogs := filepath.Join(tmpRoot, "xfs-logs")
		mustMkdirAll(t, mountPathData)
		mustMkdirAll(t, mountPathLogs)
		setTestXfsMounts(t, []string{mountPathData, mountPathLogs})

		libPathData := filepath.Join(mountPathData, "libhuatuo.so")
		libPathLogs := filepath.Join(mountPathLogs, "libhuatuo.so")
		copyCurrentExecutable(t, libPathData)
		// Hardlink: both paths resolve to the same inode on the same real fs,
		// simulating two xfs mounts that happen to expose the same inode
		// number for unrelated files.
		if err := os.Link(libPathData, libPathLogs); err != nil {
			t.Fatalf("Link(%q -> %q): %v", libPathData, libPathLogs, err)
		}

		resolver := NewUserResolver()
		if _, err := resolver.loadLibCache(processID, libPathData); err != nil {
			t.Fatalf("loadLibCache(libPathData): %v", err)
		}
		if _, err := resolver.loadLibCache(processID, libPathLogs); err != nil {
			t.Fatalf("loadLibCache(libPathLogs): %v", err)
		}
		if len(resolver.libcaches) != 2 {
			t.Errorf("loadLibCache: got %d caches, want 2 (same inode, different xfs mounts)", len(resolver.libcaches))
		}
	})
}

func TestResolveUserStackMainElf(t *testing.T) {
	resolver, processID, functionName, functionAddr := setupMainElfResolverFixture(t)

	stack := []uint64{functionAddr, functionAddr}
	got := resolver.ResolveUserStackStrs(processID, stack, 1)
	want := []string{functionName}
	if !slices.Equal(got, want) {
		t.Errorf("ResolveUserStackStrs main ELF: got %v, want %v", got, want)
	}

	byteFrames := resolver.ResolveUserStackBytes(processID, []uint64{functionAddr}, 1)
	if !slices.Equal(bytesFramesToStrings(byteFrames), []string{functionName}) {
		t.Errorf("ResolveUserStackBytes main ELF: got %v, want [%s]", bytesFramesToStrings(byteFrames), functionName)
	}
}

func TestResolveUserStackStrsLibraryFallback(t *testing.T) {
	t.Run("non-pie-single-rxp-segment", func(t *testing.T) {
		resolver, processID, functionName, stackAddr := setupLibraryResolverFixture(t)
		got := resolver.ResolveUserStackStrs(processID, []uint64{stackAddr}, 1)
		want := []string{functionName}
		if !slices.Equal(got, want) {
			t.Errorf("ResolveUserStackStrs: got %v, want %v", got, want)
		}
	})

	t.Run("pie-library-base-from-first-segment", func(t *testing.T) {
		// PIE layout: r--p (offset=0) comes before r-xp (offset=0x1000).
		// The base address must use the first segment, not the r-xp segment.
		setTestXfsMounts(t, []string{"/"})
		tmpRoot := setupTempProcRoot(t)
		processID := uint32(1001)
		procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
		rootTarget := filepath.Join(tmpRoot, "container-root")

		mustMkdirAll(t, procDir)
		mustMkdirAll(t, filepath.Join(rootTarget, "usr", "bin"))
		mustMkdirAll(t, filepath.Join(rootTarget, "usr", "lib"))
		mustSymlink(t, rootTarget, filepath.Join(procDir, "root"))

		executablePath := filepath.Join(rootTarget, "usr", "bin", "huatuo-dev")
		copyCurrentExecutable(t, executablePath)
		mustSymlink(t, "/usr/bin/huatuo-dev", filepath.Join(procDir, "exe"))

		libraryPath := filepath.Join(rootTarget, "usr", "lib", "libhuatuo-pie.so")
		copyCurrentExecutable(t, libraryPath)
		functionName, functionAddr := firstFunctionSymbol(t, libraryPath)

		// r--p (offset=0) before r-xp (offset=0x1000): PIE layout
		// r-xp range must be large enough to cover Go test binary symbol addresses
		pieBase := uint64(0x70000000)
		mapsContent := "" +
			"70000000-70001000 r--p 00000000 fd:01 2001 /usr/lib/libhuatuo-pie.so\n" +
			"70001000-80000000 r-xp 00001000 fd:01 2001 /usr/lib/libhuatuo-pie.so\n"
		mustWriteFile(t, filepath.Join(procDir, "maps"), mapsContent)

		// Runtime addr = pieBase + ELF symbol Value (first segment offset=0)
		stackAddr := pieBase + functionAddr
		resolver := NewUserResolver()
		got := resolver.ResolveUserStackStrs(processID, []uint64{stackAddr}, 1)
		want := []string{functionName}
		if !slices.Equal(got, want) {
			t.Errorf("ResolveUserStackStrs PIE: got %v, want %v", got, want)
		}
	})
}

func TestResolveAddrFailFrames(t *testing.T) {
	newResolver := func(pid uint32, inode uint64) *UserResolver {
		resolver := NewUserResolver()
		key := cacheKey{inode: inode}
		resolver.exeKeys[pid] = key
		resolver.exeCache[key] = &elfCache{
			secs: sections{
				&procfs.ProcMap{
					StartAddr: uintptr(0x1000),
					EndAddr:   uintptr(0x2000),
					Pathname:  ".text",
				},
			},
			syms: symbols{},
		}
		return resolver
	}

	tests := []struct {
		name    string
		addr    uint64
		inode   uint64
		prepare func(t *testing.T, resolver *UserResolver, pid uint32)
		want    string
	}{
		{
			name:  "elf-no-sym",
			addr:  0x1010,
			inode: 1,
			want:  "unknown elf-no-sym",
		},
		{
			name:  "procmap-fail",
			addr:  0x3000,
			inode: 2,
			prepare: func(t *testing.T, _ *UserResolver, _ uint32) {
				setupTempProcRoot(t)
			},
			want: "unknown procmap-fail",
		},
		{
			name:  "proc-unmapped",
			addr:  0x90000000,
			inode: 3,
			prepare: func(_ *testing.T, resolver *UserResolver, pid uint32) {
				resolver.procmaps[pid] = sections{
					&procfs.ProcMap{
						StartAddr: uintptr(0x70000000),
						EndAddr:   uintptr(0x71000000),
						Pathname:  "/usr/lib/libhuatuo.so",
					},
				}
			},
			want: "unknown proc-unmapped",
		},
		{
			name:  "non-lib",
			addr:  0x80000000,
			inode: 4,
			prepare: func(_ *testing.T, resolver *UserResolver, pid uint32) {
				resolver.procmaps[pid] = sections{
					&procfs.ProcMap{
						StartAddr: uintptr(0x80000000),
						EndAddr:   uintptr(0x81000000),
						Pathname:  "[heap]",
					},
				}
			},
			want: "unknown non-lib[heap]",
		},
		{
			name:  "lib-load-fail",
			addr:  0x70000000,
			inode: 5,
			prepare: func(t *testing.T, resolver *UserResolver, pid uint32) {
				setTestXfsMounts(t, []string{"/"})
				setupTempProcRoot(t)
				resolver.procmaps[pid] = sections{
					&procfs.ProcMap{
						StartAddr: uintptr(0x70000000),
						EndAddr:   uintptr(0x71000000),
						Pathname:  "/usr/lib/libhuatuo-missing.so",
					},
				}
			},
			want: "unknown lib-load-fail/usr/lib/libhuatuo-missing.so",
		},
		{
			name:  "lib-no-sym",
			addr:  0x70000010,
			inode: 6,
			prepare: func(t *testing.T, resolver *UserResolver, pid uint32) {
				setupTempProcRoot(t)

				libPathname := "/usr/lib/libhuatuo.so"
				resolver.procmaps[pid] = sections{
					&procfs.ProcMap{
						StartAddr: uintptr(0x70000000),
						EndAddr:   uintptr(0x71000000),
						Offset:    0,
						Pathname:  libPathname,
					},
				}

				libPath := filepath.Join(procfs.Path(strconv.Itoa(int(pid))+"/root"), libPathname)
				libKey := cacheKey{inode: 7}
				resolver.libKeys[libPath] = libKey
				resolver.libcaches[libKey] = &libCache{syms: symbols{}}
			},
			want: "unknown lib-no-sym/usr/lib/libhuatuo.so",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid := uint32(1001)
			resolver := newResolver(pid, tt.inode)
			if tt.prepare != nil {
				tt.prepare(t, resolver, pid)
			}

			got := resolver.resolveAddr(pid, tt.addr)
			if got != tt.want {
				t.Errorf("resolveAddr %s: got %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestResolveUserStackStrsInvalidPid(t *testing.T) {
	setTestXfsMounts(t, []string{"/"})
	resolver := NewUserResolver()

	// Invalid pid: elf-load-fail reason is encoded in the frame string.
	const wantFrame = "unknown elf-load-fail"

	got := resolver.ResolveUserStackStrs(uint32(99999998), []uint64{0x400100}, 1)
	if len(got) != 1 || got[0] != wantFrame {
		t.Errorf("ResolveUserStackStrs invalid pid: got %v, want [%s]", got, wantFrame)
	}

	byteFrames := resolver.ResolveUserStackBytes(uint32(99999998), []uint64{0x400100}, 1)
	if len(byteFrames) != 1 || string(byteFrames[0]) != wantFrame {
		t.Errorf("ResolveUserStackBytes invalid pid: got %v, want [%s]", byteFrames, wantFrame)
	}
}
