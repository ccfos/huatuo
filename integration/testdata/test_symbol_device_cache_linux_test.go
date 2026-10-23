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
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/symbol"
)

func TestSymbolDeviceCache(t *testing.T) {
	mounts := [2]string{
		os.Getenv("HUATUO_SYMBOL_DEVICE_CACHE_MOUNT0"),
		os.Getenv("HUATUO_SYMBOL_DEVICE_CACHE_MOUNT1"),
	}
	if mounts[0] == "" || mounts[1] == "" {
		t.Fatal("symbol cache fixture mount paths are not set")
	}
	for _, name := range []string{"program", "library.so"} {
		var first, second unix.Stat_t
		if err := unix.Stat(filepath.Join(mounts[0], name), &first); err != nil {
			t.Fatal(err)
		}
		if err := unix.Stat(filepath.Join(mounts[1], name), &second); err != nil {
			t.Fatal(err)
		}
		if first.Ino != second.Ino || first.Dev == second.Dev {
			t.Fatalf("%s identity: (%d,%d) and (%d,%d); want equal inodes on different devices",
				name, first.Dev, first.Ino, second.Dev, second.Ino)
		}
		var alias unix.Stat_t
		if err := unix.Stat(filepath.Join(mounts[0], name+"-alias"), &alias); err != nil {
			t.Fatal(err)
		}
		if first.Ino != alias.Ino || first.Dev != alias.Dev {
			t.Fatalf("%s alias is not a hard link", name)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	resolver := symbol.NewUsymResolver()
	for i, dir := range mounts {
		t.Run(fmt.Sprintf("image-%d", i), func(t *testing.T) {
			checkSymbolProcess(t, ctx, resolver, filepath.Join(dir, "program"), filepath.Join(dir, "library.so"), i)
		})
	}
	checkSymbolProcess(t, ctx, resolver,
		filepath.Join(mounts[0], "program-alias"), filepath.Join(mounts[0], "library.so-alias"), 0)
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
