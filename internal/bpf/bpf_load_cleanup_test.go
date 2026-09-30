//go:build !didi

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

package bpf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"golang.org/x/sys/unix"
)

func TestLoadCollectionClosesPartialClones(t *testing.T) {
	requireBPFPermission(t)
	if os.Getenv("HUATUO_TEST_LOAD_FD_LIMIT") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLoadCollectionClosesPartialClones$", "-test.v")
		cmd.Env = append(os.Environ(), "HUATUO_TEST_LOAD_FD_LIMIT=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("FD-limit subprocess: %v\n%s", err, out)
		}
		t.Logf("%s", out)
		return
	}
	// Keep finalizers from concealing missing synchronous cleanup. Only this
	// subprocess changes its GC setting or soft descriptor limit.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	spec := &ebpf.CollectionSpec{Maps: map[string]*ebpf.MapSpec{}, Programs: map[string]*ebpf.ProgramSpec{}}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("map%d", i)
		spec.Maps[name] = &ebpf.MapSpec{Name: name, Type: ebpf.Array, KeySize: 4, ValueSize: 8, MaxEntries: 1}
	}
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("prog%d", i)
		spec.Programs[name] = &ebpf.ProgramSpec{Name: name, Type: ebpf.SocketFilter, License: "GPL", Instructions: asm.Instructions{asm.Mov.Imm(asm.R0, 0), asm.Return()}}
	}
	// Warm kernel feature probes before measuring descriptor ownership.
	warm, err := LoadBPFFromCollectionSpec("cleanup", spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := warm.Close(); err != nil {
		t.Fatal(err)
	}
	countFDs := func() int {
		files, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(files)
	}
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	before := countFDs()
	sawMap, sawProgram := false, false
	// Sweep the free descriptor budget so the test does not depend on the
	// test runner's inherited descriptors or map iteration order.
	for budget := 8; budget <= 36; budget++ {
		reduced := original
		reduced.Cur = uint64(before + budget)
		if reduced.Cur > original.Cur {
			t.Fatalf("soft FD limit %d is too small", original.Cur)
		}
		if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &reduced); err != nil {
			t.Fatal(err)
		}
		object, loadErr := LoadBPFFromCollectionSpec("cleanup", spec, nil)
		restoreErr := unix.Setrlimit(unix.RLIMIT_NOFILE, &original)
		if object != nil {
			if err := object.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if after := countFDs(); after != before {
			t.Fatalf("budget=%d: before=%d after=%d, load error=%v", budget, before, after, loadErr)
		}
		if loadErr == nil {
			continue
		}
		if !errors.Is(loadErr, unix.EMFILE) && !strings.Contains(loadErr.Error(), unix.EMFILE.Error()) {
			t.Fatalf("unexpected load error: %v", loadErr)
		}
		sawMap = sawMap || strings.Contains(loadErr.Error(), "clone map:")
		sawProgram = sawProgram || strings.Contains(loadErr.Error(), "clone program:")
	}
	if !sawMap || !sawProgram {
		t.Fatalf("missing clone failure coverage: map=%t program=%t", sawMap, sawProgram)
	}
}
