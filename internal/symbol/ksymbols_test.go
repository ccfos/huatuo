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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func resetKernelSymbolFixture(t *testing.T, lines []string) {
	t.Helper()
	tmpRoot := setupTempProcRoot(t)
	procDir := filepath.Join(tmpRoot, "proc")
	mustMkdirAll(t, procDir)
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	mustWriteFile(t, filepath.Join(procDir, "kallsyms"), content)

	kernelSymbols = symbols{}
	ksymOnce = sync.Once{}
	t.Cleanup(func() {
		kernelSymbols = symbols{}
		ksymOnce = sync.Once{}
	})
}

func TestDumpKernelBackTraceStrsSmoke(t *testing.T) {
	resetKernelSymbolFixture(t, []string{"ffffffff81000000 T kernel_entry"})
	stack := []uint64{0xffffffff81000010}
	got := KsymStackStrs(stack, 1)
	want := []string{"kernel_entry/+16 [kernel]"}
	if len(got) != len(want) {
		t.Fatalf("KsymStackStrs: got %d frames, want %d", len(got), len(want))
	}
	if got[0] != want[0] {
		t.Errorf("KsymStackStrs: got %q, want %q", got[0], want[0])
	}
}

func TestDumpKernelBackTraceBytesSmoke(t *testing.T) {
	resetKernelSymbolFixture(t, []string{"ffffffff81000000 T kernel_entry"})
	stack := []uint64{0xffffffff81000010}
	got := KsymStackBytes(stack, 1)
	if len(got) != 1 {
		t.Fatalf("KsymStackBytes: got %d frames, want 1", len(got))
	}
	if string(got[0]) != "kernel_entry/+16 [kernel]" {
		t.Errorf("KsymStackBytes: got %q, want %q", string(got[0]), "kernel_entry/+16 [kernel]")
	}
}

func TestKsymbolSearchAddr(t *testing.T) {
	resetKernelSymbolFixture(t, []string{
		"ffffffff81000000 T kernel_sched_tick",
		"ffffffff81200000 t do_sys_open",
	})

	addr, err := KsymbolSearchAddr("kernel_sched_tick")
	if err != nil {
		t.Fatalf("KsymbolSearchAddr(kernel_sched_tick): %v", err)
	}
	if addr != 0xffffffff81000000 {
		t.Errorf("KsymbolSearchAddr(kernel_sched_tick): got 0x%x, want 0xffffffff81000000", addr)
	}
}

func TestKsymbolSearchAddrNotFound(t *testing.T) {
	resetKernelSymbolFixture(t, []string{"ffffffff81000000 T kernel_sched_tick"})
	missingName := "symbol-huatuo-not-found-" + strconv.Itoa(2026)
	_, err := KsymbolSearchAddr(missingName)
	if err == nil {
		t.Errorf("KsymbolSearchAddr(%q): got nil error, want non-nil", missingName)
	}
}

func TestDumpKernelBackTrace(t *testing.T) {
	tests := []struct {
		name     string
		stack    []uint64
		maxDepth int
		want     []string
	}{
		{
			name:     "full-stack-reverse-order",
			stack:    []uint64{0xffffffff81000010, 0xffffffff81100020, 0xffffffff81200030},
			maxDepth: 3,
			want: []string{
				"do_sys_open/+48 [kernel]",
				"kernel_sched_tick/+32 [kernel]",
				"kernel_entry/+16 [kernel]",
			},
		},
		{
			name:     "stop-at-first-zero",
			stack:    []uint64{0xffffffff81000010, 0x0, 0xffffffff81200030},
			maxDepth: 3,
			want:     []string{"kernel_entry/+16 [kernel]"},
		},
		{
			name:     "max-depth-limits-output",
			stack:    []uint64{0xffffffff81000010, 0xffffffff81100020, 0xffffffff81200030},
			maxDepth: 2,
			want: []string{
				"kernel_sched_tick/+32 [kernel]",
				"kernel_entry/+16 [kernel]",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetKernelSymbolFixture(t, []string{
				"ffffffff81000000 T kernel_entry",
				"ffffffff81100000 T kernel_sched_tick",
				"ffffffff81200000 t do_sys_open",
			})
			got := dumpKernelBackTrace(tt.stack, tt.maxDepth, outTypeString, true).strings
			if len(got) != len(tt.want) {
				t.Fatalf("dumpKernelBackTrace(%s): got %d frames, want %d", tt.name, len(got), len(tt.want))
			}
			for index := range tt.want {
				if got[index] != tt.want[index] {
					t.Errorf("dumpKernelBackTrace(%s)[%d]: got %q, want %q", tt.name, index, got[index], tt.want[index])
				}
			}
		})
	}
}

func TestDumpKernelBackTraceUnknownAddr(t *testing.T) {
	resetKernelSymbolFixture(t, []string{"ffffffff81000000 T kernel_entry"})
	got := dumpKernelBackTrace([]uint64{0x1000}, 1, outTypeString, true).strings
	if len(got) != 1 {
		t.Fatalf("dumpKernelBackTrace unknown: got %d frames, want 1", len(got))
	}
	if !strings.Contains(got[0], "[unknown]/+4096") {
		t.Errorf("dumpKernelBackTrace unknown: got %q, want contains [unknown]/+4096", got[0])
	}
}

func TestDumpKernelBackTraceKsymNotFound(t *testing.T) {
	// Force kernelSymbols to be empty (no defaultKsym) so floorSym returns nil,
	// triggering the ksym-not-found failFrame path.
	kernelSymbols = symbols{}
	ksymOnce = sync.Once{}
	ksymOnce.Do(func() {}) // mark done so ensureKsymsLoaded is a no-op
	t.Cleanup(func() {
		kernelSymbols = symbols{}
		ksymOnce = sync.Once{}
	})

	const addr = uint64(0xffffffff81000000)
	got := dumpKernelBackTrace([]uint64{addr}, KsymStackMaxDepth, outTypeString, true).strings
	if len(got) != 1 {
		t.Fatalf("dumpKernelBackTrace ksym-not-found: got %d frames, want 1", len(got))
	}
	want := "unknown ksym-not-found"
	if got[0] != want {
		t.Errorf("dumpKernelBackTrace ksym-not-found: got %q, want %q", got[0], want)
	}
}
