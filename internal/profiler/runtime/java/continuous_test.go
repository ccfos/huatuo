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

package java

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
)

func TestCollapsedFileCollectorConsumesEachFileOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "profile-0.collapsed")
	if err := os.WriteFile(path, []byte("first 1\n"), 0o600); err != nil {
		t.Fatalf("write collapsed output: %v", err)
	}
	opt := &AsprofSamplingOption{
		Pids:         []int{1234},
		AggrInterval: time.Second,
		StartedAt:    time.Unix(100, 0),
	}
	samples, collector := newTestCollapsedFileCollector(t, opt.Pids, map[int]string{
		1234: filepath.Join(dir, "profile-*.collapsed"),
	})
	if err := collector.scanOutputFiles(false); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(*samples) != 0 {
		t.Fatalf("first observation enqueued samples=%d", len(*samples))
	}
	if err := collector.scanOutputFiles(false); err != nil {
		t.Fatalf("stable scan: %v", err)
	}
	if len(*samples) != 1 {
		t.Fatalf("stable file enqueued samples=%d, want 1", len(*samples))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("consumed output stat error=%v, want not exist", err)
	}
	if err := collector.scanOutputFiles(false); err != nil {
		t.Fatalf("scan removed output: %v", err)
	}
	if len(*samples) != 1 {
		t.Fatalf("removed output enqueued again: samples=%d", len(*samples))
	}
}

func TestCollapsedFileCollectorRetriesRemovalWithoutDuplicateSample(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "profile-0.collapsed")
	if err := os.WriteFile(path, []byte("first 1\n"), 0o600); err != nil {
		t.Fatalf("write collapsed output: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("make output directory read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Errorf("restore output directory permissions: %v", err)
		}
	})

	samples, collector := newTestCollapsedFileCollector(t, []int{1234}, map[int]string{
		1234: filepath.Join(dir, "profile-*.collapsed"),
	})
	if err := collector.scanOutputFiles(true); err != nil {
		t.Fatalf("scan output with removal failure: %v", err)
	}
	if len(*samples) != 1 {
		t.Fatalf("enqueued samples=%d, want 1", len(*samples))
	}
	if err := collector.scanOutputFiles(true); err != nil {
		t.Fatalf("rescan retained output: %v", err)
	}
	if len(*samples) != 1 {
		t.Fatalf("retained output enqueued again: samples=%d", len(*samples))
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore output directory permissions: %v", err)
	}
	if err := os.WriteFile(path, []byte("second 22\n"), 0o600); err != nil {
		t.Fatalf("replace collapsed output: %v", err)
	}
	if err := collector.scanOutputFiles(true); err != nil {
		t.Fatalf("scan replacement output: %v", err)
	}
	if len(*samples) != 2 {
		t.Fatalf("replacement output samples=%d, want 2", len(*samples))
	}
	if (*samples)[1].Output != "second 22\n" {
		t.Fatalf("replacement output=%q, want %q", (*samples)[1].Output, "second 22\n")
	}
	if _, ok := collector.retainedFiles[path]; ok {
		t.Fatalf("retained signature for removed output %q", path)
	}
}

func TestCollapsedFileCollectorReadsFinalSequenceWithLoopFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for path, data := range map[string]string{
		filepath.Join(dir, "profile-0.collapsed"): "loop 1\n",
		filepath.Join(dir, "profile-4.collapsed"): "final 1\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatalf("write collapsed output: %v", err)
		}
	}
	samples, collector := newTestCollapsedFileCollector(t, []int{1234}, map[int]string{
		1234: filepath.Join(dir, "profile-*.collapsed"),
	})

	if err := collector.scanOutputFiles(true); err != nil {
		t.Fatalf("scan output files: %v", err)
	}
	if len(*samples) != 2 {
		t.Fatalf("enqueued samples=%d, want 2", len(*samples))
	}
	if (*samples)[0].Output != "loop 1\n" || (*samples)[1].Output != "final 1\n" {
		t.Fatalf("outputs=(%q, %q), want loop then final", (*samples)[0].Output, (*samples)[1].Output)
	}
}

func TestCollapsedFileSequence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		path      string
		want      uint64
		wantError bool
	}{
		{
			name: "valid sequence",
			path: "/tmp/huatuo-asprof-session-cpu-1234-63.collapsed",
			want: 63,
		},
		{name: "missing sequence", path: "/tmp/profile-final.collapsed", wantError: true},
		{name: "invalid sequence", path: "/tmp/profile-x.collapsed", wantError: true},
		{name: "large sequence", path: "/tmp/profile-1024.collapsed", want: 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := collapsedFileSequence(tt.path)
			if tt.wantError {
				if err == nil {
					t.Errorf("collapsedFileSequence() error=nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Errorf("collapsedFileSequence() error=%v, want nil", err)
				return
			}
			if got != tt.want {
				t.Errorf("collapsedFileSequence()=%d, want %d", got, tt.want)
			}
		})
	}
}

func BenchmarkCollapsedFileSequence(b *testing.B) {
	path := "/tmp/huatuo-asprof-session-cpu-1234-63.collapsed"
	b.ReportAllocs()
	for range b.N {
		if _, err := collapsedFileSequence(path); err != nil {
			b.Fatal(err)
		}
	}
}

func newTestCollapsedFileCollector(
	t *testing.T,
	pids []int,
	pidsToFilePath map[int]string,
) (*[]profiler.SampleOutput, *collapsedFileCollector) {
	t.Helper()
	if pidsToFilePath == nil {
		pidsToFilePath = make(map[int]string, len(pids))
		for _, pid := range pids {
			pidsToFilePath[pid] = filepath.Join(t.TempDir(), "profile-*.collapsed")
		}
	}
	samples := &[]profiler.SampleOutput{}
	collector := newCollapsedFileCollector(
		pids,
		pidsToFilePath,
		func(sample profiler.SampleOutput) { *samples = append(*samples, sample) },
	)
	return samples, collector
}
