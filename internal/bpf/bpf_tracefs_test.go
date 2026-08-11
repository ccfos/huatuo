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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanKprobeFunctions(t *testing.T) {
	cache := make(map[string]struct{})
	input := "  mutex_lock\n_raw_spin_lock   [kernel]\n\n_raw_read_lock\t[kernel]\n"
	if err := scanKprobeFunctions(strings.NewReader(input), cache); err != nil {
		t.Fatalf("scanKprobeFunctions() error = %v", err)
	}
	for _, symbol := range []string{"mutex_lock", "_raw_spin_lock", "_raw_read_lock"} {
		if _, ok := cache[symbol]; !ok {
			t.Errorf("symbol %q missing from cache", symbol)
		}
	}
}

func TestHasKprobeFunctionChecksTracefsAndDebugfs(t *testing.T) {
	dir := t.TempDir()
	tracefs := filepath.Join(dir, "tracefs")
	debugfs := filepath.Join(dir, "debugfs")
	if err := os.WriteFile(tracefs, []byte("tracefs_symbol\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(debugfs, []byte("debugfs_symbol [kernel]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resetKprobeCacheForTest(t, []string{tracefs, debugfs})

	for _, symbol := range []string{"tracefs_symbol", "debugfs_symbol"} {
		if !HasKprobeFunction(symbol) {
			t.Errorf("HasKprobeFunction(%q) = false", symbol)
		}
	}
}

func TestHasKprobeFunctionRetriesFailedReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "available_filter_functions")
	resetKprobeCacheForTest(t, []string{path})

	if HasKprobeFunction("late_symbol") {
		t.Fatal("missing symbol reported as available")
	}
	if err := os.WriteFile(path, []byte("late_symbol\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasKprobeFunction("late_symbol") {
		t.Fatal("symbol was not found after the source became readable")
	}
}

func TestTracepointAvailable(t *testing.T) {
	availableRoot := t.TempDir()
	tracepointID := filepath.Join(
		availableRoot,
		"events",
		"devlink",
		"devlink_trap_report",
		"id",
	)
	if err := os.MkdirAll(filepath.Dir(tracepointID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracepointID, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name             string
		roots            []string
		want             bool
		wantErrSubstring string
	}{
		{
			name:  "available in fallback root",
			roots: []string{t.TempDir(), availableRoot},
			want:  true,
		},
		{
			name:  "unavailable",
			roots: []string{t.TempDir(), t.TempDir()},
		},
		{
			name:             "stat error",
			roots:            []string{"\x00"},
			wantErrSubstring: "check tracepoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTraceFSRootsForTest(t, tt.roots)

			got, err := TracepointAvailable("devlink", "devlink_trap_report")
			if tt.wantErrSubstring != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSubstring) {
					t.Fatalf("TracepointAvailable() error = %v, want %q", err, tt.wantErrSubstring)
				}
				return
			}
			if err != nil {
				t.Fatalf("TracepointAvailable() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("TracepointAvailable() = %t, want %t", got, tt.want)
			}
		})
	}
}

func resetKprobeCacheForTest(t *testing.T, paths []string) {
	t.Helper()

	kprobeOnce.Lock()
	oldFiles := kprobeFunctionFiles
	oldCache := kprobeCache
	oldCached := kprobeCached
	kprobeFunctionFiles = paths
	kprobeCache = nil
	kprobeCached = false
	kprobeOnce.Unlock()

	t.Cleanup(func() {
		kprobeOnce.Lock()
		defer kprobeOnce.Unlock()
		kprobeFunctionFiles = oldFiles
		kprobeCache = oldCache
		kprobeCached = oldCached
	})
}

func resetTraceFSRootsForTest(t *testing.T, roots []string) {
	t.Helper()

	oldRoots := traceFSRoots
	traceFSRoots = roots
	t.Cleanup(func() {
		traceFSRoots = oldRoots
	})
}
