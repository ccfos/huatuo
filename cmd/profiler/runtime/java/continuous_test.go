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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/profiling/profile"
)

func TestCollapsedFileCollectorConsumesEachFileOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "profile-0.collapsed")
	if err := os.WriteFile(path, []byte("first 1\n"), 0o600); err != nil {
		t.Fatalf("write collapsed output: %v", err)
	}

	samples, collector, enqueue := newTestCollapsedFileCollector(t, []int{1234}, map[int]string{
		1234: filepath.Join(dir, "profile-*.collapsed"),
	})
	if err := collector.scanOutputFiles(t.Context(), false, enqueue); err != nil {
		t.Fatalf("first scan: %v", err)
	}

	if len(*samples) != 0 {
		t.Fatalf("first observation enqueued samples=%d", len(*samples))
	}

	if err := collector.scanOutputFiles(t.Context(), false, enqueue); err != nil {
		t.Fatalf("stable scan: %v", err)
	}

	if len(*samples) != 1 {
		t.Fatalf("stable file enqueued samples=%d, want 1", len(*samples))
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("consumed output stat error=%v, want not exist", err)
	}

	if err := collector.scanOutputFiles(t.Context(), false, enqueue); err != nil {
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

	samples, collector, enqueue := newTestCollapsedFileCollector(t, []int{1234}, map[int]string{
		1234: filepath.Join(dir, "profile-*.collapsed"),
	})
	if err := collector.scanOutputFiles(t.Context(), true, enqueue); err != nil {
		t.Fatalf("scan output with removal failure: %v", err)
	}

	if len(*samples) != 1 {
		t.Fatalf("enqueued samples=%d, want 1", len(*samples))
	}

	if err := collector.scanOutputFiles(t.Context(), true, enqueue); err != nil {
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

	if err := collector.scanOutputFiles(t.Context(), true, enqueue); err != nil {
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

	samples, collector, enqueue := newTestCollapsedFileCollector(t, []int{1234}, map[int]string{
		1234: filepath.Join(dir, "profile-*.collapsed"),
	})

	if err := collector.scanOutputFiles(t.Context(), true, enqueue); err != nil {
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
		{
			name:      "missing sequence",
			path:      "/tmp/profile-final.collapsed",
			wantError: true,
		},
		{
			name:      "invalid sequence",
			path:      "/tmp/profile-x.collapsed",
			wantError: true,
		},
		{
			name: "large sequence",
			path: "/tmp/profile-1024.collapsed",
			want: 1024,
		},
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
) (*[]profile.SampleOutput, *CollapsedFileCollector, func(profile.SampleOutput) error) {
	t.Helper()
	if pidsToFilePath == nil {
		pidsToFilePath = make(map[int]string, len(pids))
		for _, pid := range pids {
			pidsToFilePath[pid] = filepath.Join(t.TempDir(), "profile-*.collapsed")
		}
	}

	var samples []profile.SampleOutput
	enqueue := func(sample profile.SampleOutput) error {
		samples = append(samples, sample)
		return nil
	}

	return &samples, NewCollapsedFileCollector(pids, pidsToFilePath), enqueue
}

func TestCollapsedFileCollectorFinalization(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile-0.collapsed")
			if err := os.WriteFile(path, []byte("root;leaf 7\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			count := 0
			enqueue := func(profile.SampleOutput) error { count++; return nil }
			c := NewCollapsedFileCollector([]int{123}, map[int]string{123: path})
			collectCtx, stop := context.WithCancel(t.Context())
			stop()
			if err := c.ReadDataLoop(collectCtx, enqueue); err != nil {
				t.Fatal(err)
			}

			if count != 0 {
				t.Fatal("final file consumed during stopped read loop")
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if canceled {
				cancel()
			}

			err := c.Finalize(ctx, &AsprofSamplingOption{Pids: []int{123}}, enqueue)
			if canceled {
				if !errors.Is(err, context.Canceled) || count != 0 {
					t.Fatalf("error=%v samples=%d", err, count)
				}
			} else if err != nil || count != 1 {
				t.Fatalf("error=%v samples=%d", err, count)
			}
		})
	}
}

func TestCollapsedFileCollectorOwnsTargets(t *testing.T) {
	original := filepath.Join(t.TempDir(), "profile-0.collapsed")
	replacement := filepath.Join(t.TempDir(), "profile-1.collapsed")
	if err := os.WriteFile(original, []byte("original 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(replacement, []byte("replacement 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pids := []int{123}
	paths := map[int]string{123: original}
	c := NewCollapsedFileCollector(pids, paths)
	pids[0] = 456
	paths[123] = replacement
	var samples []profile.SampleOutput
	err := c.Finalize(t.Context(), &AsprofSamplingOption{}, func(sample profile.SampleOutput) error {
		samples = append(samples, sample)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(samples) != 1 || samples[0].PID != 123 || samples[0].Output != "original 7\n" {
		t.Fatalf("final samples=%+v, want original target PID 123", samples)
	}

	if _, err := os.Stat(replacement); err != nil {
		t.Fatalf("caller-owned replacement was consumed: %v", err)
	}
}
