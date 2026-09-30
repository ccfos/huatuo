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
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ccfos/huatuo/internal/procfs"
)

func TestResolveRunningExecutableAfterUpgrade(t *testing.T) {
	compiler, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang is required for real ELF process fixtures")
	}
	setTestXfsMounts(t, nil)
	for _, pie := range []bool{false, true} {
		for _, replace := range []bool{false, true} {
			t.Run(fmt.Sprintf("pie=%t/replace=%t", pie, replace), func(t *testing.T) {
				dir := t.TempDir()
				source := filepath.Join(dir, "probe.c")
				binary := filepath.Join(dir, "running-app")
				mustWriteFile(t, source, `#include <stdio.h>
void huatuo_symbol_probe(void) { puts("probe"); }
int main(void) {
 printf("%p\n", (void *)huatuo_symbol_probe);
 fflush(stdout);
 getchar();
 return 0;
}
`)
				args := []string{"-O0", "-g", "-o", binary, source, "-fno-pie", "-no-pie"}
				if pie {
					args = []string{"-O0", "-g", "-o", binary, source, "-fPIE", "-pie"}
				}
				if output, err := exec.Command(compiler, args...).CombinedOutput(); err != nil {
					t.Fatalf("compile: %v: %s", err, output)
				}
				cmd := exec.Command(binary)
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = stdin.Close()
					if err := cmd.Wait(); err != nil {
						t.Error(err)
					}
				})
				scanner := bufio.NewScanner(stdout)
				if !scanner.Scan() {
					t.Fatalf("read child address: %v", scanner.Err())
				}
				addr, err := strconv.ParseUint(scanner.Text(), 0, 64)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(binary); err != nil {
					t.Fatal(err)
				}
				if replace {
					if err := os.WriteFile(binary, []byte("replacement is not the running ELF"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				// Resolve without priming the cache before unlink or replacement.
				resolver := NewUsymResolver()
				got := resolver.UsymStackStrs(uint32(cmd.Process.Pid), []uint64{addr}, 1)
				if len(got) != 1 || got[0] != "huatuo_symbol_probe" {
					t.Fatalf("running function = %v, want huatuo_symbol_probe", got)
				}
			})
		}
	}
}

func TestRunningExecutableCacheKeepsXFSMountIdentity(t *testing.T) {
	root := setupTempProcRoot(t)
	pid := uint32(1001)
	setupHostProcessProcFS(t, root, pid)
	binary := filepath.Join(root, "binary")
	copyCurrentExecutable(t, binary)
	mustSymlink(t, binary, filepath.Join(root, "proc", "1001", "exe"))
	setTestXfsMounts(t, []string{root, "/unrelated-xfs"})
	resolver := NewUsymResolver()
	if _, err := resolver.loadElfCaches(pid); err != nil {
		t.Fatal(err)
	}
	if got := resolver.exeKeys[pid].mountKey; got != root {
		t.Errorf("XFS identity = %q, want %q", got, root)
	}
}

func BenchmarkCachedExecutableSymbol(b *testing.B) {
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
