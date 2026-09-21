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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/procfs"
)

func TestSymbolCachesSeparateFilesystemDevices(t *testing.T) {
	compiler, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang is required for ELF fixtures")
	}
	setTestXfsMounts(t, nil)
	paths := make([]string, 2)
	names := []string{"first_image_function", "second_image_function"}
	addresses := make([]uint64, 2)
	identities := make([]unix.Stat_t, 2)
	for i := range paths {
		mount := t.TempDir()
		if err := unix.Mount("tmpfs", mount, "tmpfs", 0, "size=16m"); err != nil {
			if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
				t.Skip("CAP_SYS_ADMIN is required for separate tmpfs fixtures")
			}
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := unix.Unmount(mount, 0); err != nil {
				t.Error(err)
			}
		})
		source := filepath.Join(t.TempDir(), "image.c")
		mustWriteFile(t, source, fmt.Sprintf("int %s(void) { return %d; }\n", names[i], i))
		paths[i] = filepath.Join(mount, "image.so")
		if output, err := exec.Command(compiler, "-shared", "-fPIC", "-o", paths[i], source).CombinedOutput(); err != nil {
			t.Fatalf("compile: %v: %s", err, output)
		}
		if err := unix.Stat(paths[i], &identities[i]); err != nil {
			t.Fatal(err)
		}
		f, err := elf.Open(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		symbols, err := f.DynamicSymbols()
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, sym := range symbols {
			if sym.Name == names[i] {
				addresses[i] = sym.Value
			}
		}
		if addresses[i] == 0 {
			t.Fatalf("fixture function %s not found", names[i])
		}
	}
	if identities[0].Ino != identities[1].Ino || identities[0].Dev == identities[1].Dev {
		t.Fatalf("fixtures must share an inode on different devices: %+v, %+v", identities[0], identities[1])
	}
	resolver := NewUsymResolver()
	for _, keyFor := range []struct {
		name string
		key  func(uint32, string) (cacheKey, error)
	}{
		{"executable", resolver.exeCacheKey}, {"library", resolver.libCacheKey},
	} {
		first, err := keyFor.key(123, paths[0])
		if err != nil {
			t.Fatal(err)
		}
		second, err := keyFor.key(123, paths[1])
		if err != nil {
			t.Fatal(err)
		}
		if first == second {
			t.Errorf("%s keys collide for different devices", keyFor.name)
		}
	}
	for i, path := range paths {
		cache, err := resolver.loadLibCache(123, path)
		if err != nil {
			t.Fatal(err)
		}
		if got := cache.syms.resolve(addresses[i]); got != names[i] {
			t.Errorf("image %d resolved %q, want %q", i, got, names[i])
		}
	}
	alias := filepath.Join(filepath.Dir(paths[0]), "hardlink.so")
	if err := os.Link(paths[0], alias); err != nil {
		t.Fatal(err)
	}
	original, err := resolver.loadLibCache(123, paths[0])
	if err != nil {
		t.Fatal(err)
	}
	linked, err := resolver.loadLibCache(123, alias)
	if err != nil {
		t.Fatal(err)
	}
	if original != linked {
		t.Error("hard links must share the same parsed ELF")
	}
}

func BenchmarkDeviceAwareCachedSymbol(b *testing.B) {
	resolver := NewUsymResolver()
	key := cacheKey{inode: 1}
	resolver.exeKeys[1] = key
	resolver.exeCache[key] = &elfCache{
		secs: sections{&procfs.ProcMap{StartAddr: 0x1000, EndAddr: 0x2000, Pathname: ".text"}},
		syms: symbols{&symbol{Addr: 0x1000, Size: 0x1000, Name: "probe"}},
	}
	b.ReportAllocs()
	for b.Loop() {
		resolver.resolveAddr(1, 0x1001)
	}
}
