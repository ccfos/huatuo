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

//go:build integration && linux

package integration

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/symbol"
)

func TestSymbolDeviceCache(t *testing.T) {
	for _, tool := range []string{"clang", "mke2fs", "mount"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("integration prerequisite %s: %v", tool, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	sources := t.TempDir()
	programSource := filepath.Join(sources, "program.c")
	writeSymbolFixture(t, programSource, []byte(`#include <dlfcn.h>
#include <stdio.h>
void ENTRY(void) {}
int main(int argc, char **argv) {
 if (argc != 3) return 1;
 void *handle = dlopen(argv[1], RTLD_NOW);
 if (!handle) return 2;
 void *function = dlsym(handle, argv[2]);
 if (!function) return 3;
 printf("%p %p\n", (void *)ENTRY, function);
 fflush(stdout);
 getchar();
 dlclose(handle);
 return 0;
}
`))
	librarySource := filepath.Join(sources, "library.c")
	writeSymbolFixture(t, librarySource, []byte("void ENTRY(void) {}\n"))
	directories := [2]string{t.TempDir(), t.TempDir()}
	for i, dir := range directories {
		runSymbolCommand(t, ctx, "clang", "-O0", "-g", "-fno-pie", "-no-pie",
			fmt.Sprintf("-DENTRY=image_%d_entry", i), "-o", filepath.Join(dir, "program"), programSource, "-ldl")
		runSymbolCommand(t, ctx, "clang", "-shared", "-fPIC", fmt.Sprintf("-DENTRY=image_%d_library", i),
			"-o", filepath.Join(dir, "library.so"), librarySource)
	}
	// Clone an on-disk filesystem: tmpfs inode allocation differs across kernels.
	// Overwriting the second image's files preserves the cloned inode numbers.
	imageDir := t.TempDir()
	firstImage := filepath.Join(imageDir, "first.img")
	image, err := os.Create(firstImage)
	if err != nil {
		t.Fatal(err)
	}
	if err := image.Truncate(16 << 20); err != nil {
		_ = image.Close()
		t.Fatal(err)
	}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
	runSymbolCommand(t, ctx, "mke2fs", "-q", "-F", "-t", "ext2", "-m", "0", "-d", directories[0], firstImage)
	secondImage := filepath.Join(imageDir, "second.img")
	copySymbolFixture(t, firstImage, secondImage)
	mounts := [2]string{mountSymbolImage(t, ctx, firstImage), mountSymbolImage(t, ctx, secondImage)}
	for _, name := range []string{"program", "library.so"} {
		copySymbolFixture(t, filepath.Join(directories[1], name), filepath.Join(mounts[1], name))
		var a, b unix.Stat_t
		if err := unix.Stat(filepath.Join(mounts[0], name), &a); err != nil {
			t.Fatal(err)
		}
		if err := unix.Stat(filepath.Join(mounts[1], name), &b); err != nil {
			t.Fatal(err)
		}
		if a.Ino != b.Ino || a.Dev == b.Dev {
			t.Fatalf("%s identity: (%d,%d) and (%d,%d); want equal inodes on different devices", name, a.Dev, a.Ino, b.Dev, b.Ino)
		}
	}
	resolver := symbol.NewUsymResolver()
	for i, dir := range mounts {
		t.Run(fmt.Sprintf("image-%d", i), func(t *testing.T) {
			checkSymbolProcess(t, ctx, resolver, filepath.Join(dir, "program"), filepath.Join(dir, "library.so"), i)
		})
	}
	// A second process using hard links must still resolve the original image.
	for _, name := range []string{"program", "library.so"} {
		if err := os.Link(filepath.Join(mounts[0], name), filepath.Join(mounts[0], name+"-alias")); err != nil {
			t.Fatal(err)
		}
	}
	checkSymbolProcess(t, ctx, resolver, filepath.Join(mounts[0], "program-alias"), filepath.Join(mounts[0], "library.so-alias"), 0)
}

func checkSymbolProcess(t *testing.T, ctx context.Context, resolver *symbol.UsymResolver, program, library string, index int) {
	t.Helper()
	cmd := exec.CommandContext(ctx, program, library, fmt.Sprintf("image_%d_library", index))
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
	defer func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("fixture process: %v", err)
		}
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("read fixture addresses: %v", scanner.Err())
	}
	var executableAddress, libraryAddress uint64
	if _, err := fmt.Sscanf(scanner.Text(), "0x%x 0x%x", &executableAddress, &libraryAddress); err != nil {
		t.Fatal(err)
	}
	for i, address := range []uint64{executableAddress, libraryAddress} {
		kind := []string{"entry", "library"}[i]
		want := fmt.Sprintf("image_%d_%s", index, kind)
		got := resolver.UsymStackStrs(uint32(cmd.Process.Pid), []uint64{address}, 1)
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want %q", kind, got, want)
		}
	}
}

func mountSymbolImage(t *testing.T, ctx context.Context, image string) string {
	t.Helper()
	directory := t.TempDir()
	runSymbolCommand(t, ctx, "mount", "-t", "ext4", "-o", "loop", image, directory)
	t.Cleanup(func() {
		if err := unix.Unmount(directory, 0); err != nil {
			t.Errorf("unmount fixture: %v", err)
		}
	})
	return directory
}

func copySymbolFixture(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, data)
	closeErr := output.Close()
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func writeSymbolFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runSymbolCommand(t *testing.T, ctx context.Context, command string, args ...string) {
	t.Helper()
	if output, err := exec.CommandContext(ctx, command, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", command, args, err, output)
	}
}
