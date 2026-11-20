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
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
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

	ksymTable.Store(nil)
	ksymLastFailure.Store(nil)
	t.Cleanup(func() {
		ksymTable.Store(nil)
		ksymLastFailure.Store(nil)
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

// Ordinary lookups retain the existing path; data lookups do not expand it.
func TestKsymbolSearchAddr(t *testing.T) {
	resetKernelSymbolFixture(t, []string{
		"ffffffff81001000 T core_function",
		"ffffffff81002000 T next_function",
		"ffffffff82001000 b tfms_inited",
		"ffffffff82002000 B blkcg_root",
		"ffffffff82003000 d blk_errors",
		"ffffffff82004000 D initialized_data",
		"ffffffff82005000 r local_readonly",
		"ffffffff82006000 b tfms_inited",
		"0000000000000000 b hidden_data",
		"not-hex T invalid",
		"ffffffffc0001000 t sd_open [sd_mod]",
	})
	want := map[string]KsymbolRange{
		"core_function": {Start: 0xffffffff81001000},
		"sd_open":       {Start: 0xffffffffc0001000},
	}
	names := []string{
		"core_function", "tfms_inited", "blkcg_root", "blk_errors", "initialized_data",
		"local_readonly", "hidden_data", "sd_open", "missing", "invalid",
	}
	got, err := KsymbolSearchAddr(names, KsymbolLookupDefault)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses = %#v, %v; want %#v", got, err, want)
	}

	wantData := map[string]KsymbolRange{
		"core_function":    {Start: 0xffffffff81001000},
		"tfms_inited":      {Start: 0xffffffff82001000},
		"blkcg_root":       {Start: 0xffffffff82002000},
		"blk_errors":       {Start: 0xffffffff82003000},
		"initialized_data": {Start: 0xffffffff82004000},
		"local_readonly":   {Start: 0xffffffff82005000},
		"hidden_data":      {},
		"sd_open":          {Start: 0xffffffffc0001000},
	}
	got, err = KsymbolSearchAddr(names, KsymbolLookupData)
	if err != nil || !reflect.DeepEqual(got, wantData) {
		t.Fatalf("data addresses = %#v, %v; want %#v", got, err, wantData)
	}
	got, err = KsymbolSearchAddr(names, KsymbolLookupDefault)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary addresses after data lookup = %#v, %v; want %#v", got, err, want)
	}

	mustWriteFile(t, procfs.Path("kallsyms"), "ffffffffc0002000 t sd_open [sd_mod]\n")
	got, err = KsymbolSearchAddr([]string{"sd_open"}, KsymbolLookupData)
	if err != nil || got["sd_open"].Start != 0xffffffffc0002000 {
		t.Fatalf("current data address = %#v, %v", got, err)
	}
	got, err = KsymbolSearchAddr([]string{"sd_open"}, KsymbolLookupDefault)
	if err != nil || got["sd_open"].Start != 0xffffffffc0001000 {
		t.Fatalf("existing module address = %#v, %v", got, err)
	}
}

func TestKsymbolSearchAddrTextRanges(t *testing.T) {
	resetKernelSymbolFixture(t, []string{
		"ffffffff81001000 T blk_throtl_bio",
		"ffffffff81001000 t blk_throtl_bio_alias",
		"ffffffff81001080 R blk_throtl_rodata",
		"ffffffff81001200 w weak_after",
		"ffffffff81001400 W weak_target",
		"ffffffff81001600 t tg_dispatch_one_bio",
		"ffffffff81001800 T throtl_add_bio_tg",
		"ffffffff81001a00 t throtl_pop_queued",
		"ffffffff81001c00 T del_gendisk",
	})

	got, err := KsymbolSearchAddr(
		[]string{
			"blk_throtl_bio",
			"blk_throtl_bio_alias",
			"weak_target",
			"tg_dispatch_one_bio",
			"missing_range",
		},
		KsymbolLookupRange,
	)
	if err != nil {
		t.Fatalf("KsymbolSearchAddr: %v", err)
	}
	want := map[string]KsymbolRange{
		"blk_throtl_bio": {
			Start: 0xffffffff81001000,
			End:   0xffffffff81001080,
		},
		"blk_throtl_bio_alias": {
			Start: 0xffffffff81001000,
			End:   0xffffffff81001080,
		},
		"weak_target": {
			Start: 0xffffffff81001400,
			End:   0xffffffff81001600,
		},
		"tg_dispatch_one_bio": {
			Start: 0xffffffff81001600,
			End:   0xffffffff81001800,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("KsymbolSearchAddr: got %#v, want %#v", got, want)
	}
}

func TestKsymbolSearchAddrTextRangesAllowsMissingTargets(t *testing.T) {
	resetKernelSymbolFixture(t, []string{
		"ffffffff81001000 R non_text",
		"ffffffff81002000 T attach_only",
		"ffffffff81003000 t open_range",
		"ffffffffc0004000 t module_only [vendor_probe]",
	})

	got, err := KsymbolSearchAddr(
		[]string{"attach_only", "module_only", "non_text", "open_range", "missing_range"},
		KsymbolLookupRange,
	)
	if err != nil {
		t.Fatalf("KsymbolSearchAddr: %v", err)
	}
	want := map[string]KsymbolRange{
		"non_text":    {Start: 0xffffffff81001000, End: 0xffffffff81002000},
		"attach_only": {Start: 0xffffffff81002000, End: 0xffffffff81003000},
		"open_range":  {Start: 0xffffffff81003000},
		"module_only": {Start: 0xffffffffc0004000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("KsymbolSearchAddr: got %#v, want %#v", got, want)
	}
}

func TestKsymbolSearchAddrDirectReadFailure(t *testing.T) {
	resetKernelSymbolFixture(t, nil)
	path := procfs.Path("kallsyms")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []KsymbolLookupMode{KsymbolLookupData, KsymbolLookupRange} {
		got, err := KsymbolSearchAddr([]string{"target"}, mode)
		if !errors.Is(err, os.ErrNotExist) || got != nil {
			t.Fatalf("mode %d missing file: got %#v, %v", mode, got, err)
		}
	}
	mustWriteFile(t, path, "ffffffff81001000 T target\nffffffff81002000 T next\n")
	for _, mode := range []KsymbolLookupMode{KsymbolLookupData, KsymbolLookupRange} {
		got, err := KsymbolSearchAddr([]string{"target"}, mode)
		if err != nil || got["target"].Start != 0xffffffff81001000 {
			t.Fatalf("mode %d restored file: got %#v, %v", mode, got, err)
		}
	}
}

func TestKsymbolCacheRecoversAfterLoadFailure(t *testing.T) {
	resetKernelSymbolFixture(t, nil)
	path := procfs.Path("kallsyms")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got, err := KsymbolSearchAddr([]string{"target"}, KsymbolLookupDefault)
	if !errors.Is(err, os.ErrNotExist) || got != nil {
		t.Errorf("missing symbol file: got %#v, %v", got, err)
	}
	stack := KsymStackStrs([]uint64{0xffffffff81001010}, 1)
	if len(stack) != 1 || stack[0] != "unknown ksym-not-found" {
		t.Fatalf("unavailable symbol table: got %q", stack)
	}

	mustWriteFile(t, path, "ffffffff81001000 T target\nffffffff81002000 T next\n")
	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			got, err := KsymbolSearchAddr([]string{"target"}, KsymbolLookupDefault)
			if err != nil || got["target"].Start != 0xffffffff81001000 {
				t.Errorf("restored symbol file: got %#v, %v", got, err)
			}
			stack := KsymStackStrs([]uint64{0xffffffff81001010}, 1)
			if len(stack) != 1 || stack[0] != "target/+16 [kernel]" {
				t.Errorf("restored symbol table: got %q", stack)
			}
		}()
	}
	readers.Wait()
}

func TestKsymbolStackLoadBackoff(t *testing.T) {
	resetKernelSymbolFixture(t, nil)
	path := procfs.Path("kallsyms")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(os.Stdout) })

	stack := []uint64{0xffffffff81001010}
	started := time.Now()
	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 64 {
				got := KsymStackStrs(stack, 1)
				if len(got) != 1 || got[0] != "unknown ksym-not-found" {
					t.Errorf("unavailable symbol table: got %q", got)
					return
				}
			}
		}()
	}
	readers.Wait()
	warnings := strings.Count(output.String(), "failed to load kallsyms")
	limit := 1 + int(time.Since(started)/time.Second)
	if warnings == 0 || warnings > limit {
		t.Fatalf("stack load failure warnings = %d, want between 1 and %d", warnings, limit)
	}

	mustWriteFile(t, path, "ffffffff81001000 T target\nffffffff81002000 T next\n")
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := KsymStackStrs(stack, 1)
		if len(got) == 1 && got[0] == "target/+16 [kernel]" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stack lookup did not recover: got %q", got)
		}
		time.Sleep(10 * time.Millisecond)
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
		{
			name:     "negative-depth-returns-empty",
			stack:    []uint64{0xffffffff81000010},
			maxDepth: -1,
		},
		{
			name:     "zero-depth-returns-empty",
			stack:    []uint64{0xffffffff81000010},
			maxDepth: 0,
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
	// Force ksymTable to be empty (no ksymUnknown) so floorSym returns nil,
	// triggering the ksym-not-found failFrame path.
	empty := symbols{}
	ksymTable.Store(&empty)
	t.Cleanup(func() {
		ksymTable.Store(nil)
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
