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

package autotracing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func writeSubgroupProcesses(t *testing.T, directory, members string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMemoryEventsForTest(t, filepath.Join(directory, "cgroup.procs"), members)
}

func TestProcessSelectorIncludesDescendants(t *testing.T) {
	for _, test := range []struct{ name, direct string }{
		{"empty parent", ""},
		{"smaller parent process", "11\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, group := newProcessSelectorForTest(t)
			directory := s.source.memcgDir(group.Path)
			writeSubgroupProcesses(t, directory, test.direct)
			writeSubgroupProcesses(t, filepath.Join(directory, "workers"), "")
			writeSubgroupProcesses(t, filepath.Join(directory, "workers", "child"), "22\n")
			writeProcessForTest(t, s.procRoot, memsnapshot.ProcessInstance{TGID: 11, StartTimeTicks: 100}, 100, 0)
			identity := memsnapshot.ProcessInstance{TGID: 22, StartTimeTicks: 200}
			writeProcessForTest(t, s.procRoot, identity, 200, 0)

			selected, err := s.Select(t.Context(), group, 1<<20)
			if err != nil || selected.identity != identity {
				t.Fatalf("selected = %+v, %v, want descendant %+v", selected, err, identity)
			}
			if err := s.Validate(t.Context(), group, identity); err != nil {
				t.Fatalf("validate descendant: %v", err)
			}

			writeMemoryEventsForTest(t, filepath.Join(directory, "workers", "child", "cgroup.procs"), "")
			writeSubgroupProcesses(t, s.source.memcgDir("/outside"), "22\n")
			if err := s.Validate(t.Context(), group, identity); !errors.Is(err, errInvalidSnapshotProcess) {
				t.Fatalf("process outside subtree accepted: %v", err)
			}
		})
	}
}

func TestProcessSelectorSubtreeBudget(t *testing.T) {
	for _, test := range []struct {
		name, members string
	}{
		{"processes", strings.Repeat("1\n", maxCgroupProcesses/2+1)},
		{"bytes", strings.Repeat("00000000000000001\n", 2000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, group := newProcessSelectorForTest(t)
			directory := s.source.memcgDir(group.Path)
			writeSubgroupProcesses(t, directory, test.members)
			writeSubgroupProcesses(t, filepath.Join(directory, "child"), test.members)
			if err := s.scanProcesses(t.Context(), group, func(int) error { return nil }); err == nil {
				t.Fatal("accepted an oversized subtree")
			}
		})
	}
}

func TestProcessSelectorSubtreeDirectoryBudget(t *testing.T) {
	s, group := newProcessSelectorForTest(t)
	directory := s.source.memcgDir(group.Path)
	writeSubgroupProcesses(t, directory, "")
	for index := range maxCgroupDirectories {
		writeSubgroupProcesses(t, filepath.Join(directory, strconv.Itoa(index)), "")
	}
	if err := s.scanProcesses(t.Context(), group, func(int) error { return nil }); err == nil {
		t.Fatal("accepted too many empty descendant cgroups")
	}
}

func TestProcessSelectorDoesNotFollowSymlinks(t *testing.T) {
	s, group := newProcessSelectorForTest(t)
	directory := s.source.memcgDir(group.Path)
	writeSubgroupProcesses(t, directory, "")
	writeSubgroupProcesses(t, s.source.memcgDir("/outside"), "22\n")
	if err := os.Symlink(s.source.memcgDir("/outside"), filepath.Join(directory, "outside")); err != nil {
		t.Fatal(err)
	}
	visits := 0
	if err := s.scanProcesses(t.Context(), group, func(int) error { visits++; return nil }); err != nil || visits != 0 {
		t.Fatalf("symlink escaped subtree: visits=%d err=%v", visits, err)
	}
}

func TestProcessSelectorCancellationInDescendant(t *testing.T) {
	s, group := newProcessSelectorForTest(t)
	directory := s.source.memcgDir(group.Path)
	writeSubgroupProcesses(t, directory, "")
	writeSubgroupProcesses(t, filepath.Join(directory, "child"), "22\n23\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	visits := 0
	err := s.scanProcesses(ctx, group, func(int) error {
		visits++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || visits != 1 {
		t.Fatalf("cancellation: visits=%d err=%v", visits, err)
	}
}

func TestProcessSelectorLegacyHierarchy(t *testing.T) {
	for _, value := range []string{"0\n", "1\n"} {
		t.Run(strings.TrimSpace(value), func(t *testing.T) {
			s, group := newProcessSelectorForTest(t)
			directory := s.source.memcgDir(group.Path)
			writeSubgroupProcesses(t, directory, "11\n")
			writeSubgroupProcesses(t, filepath.Join(directory, "child"), "22\n")
			writeMemoryEventsForTest(t, filepath.Join(directory, "memory.use_hierarchy"), value)
			var visits []int
			if err := s.scanProcesses(t.Context(), group, func(pid int) error {
				visits = append(visits, pid)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			want := 1
			if value == "1\n" {
				want = 2
			}
			if len(visits) != want || visits[0] != 11 {
				t.Fatalf("visits = %v, want %d processes", visits, want)
			}
		})
	}
}

func TestProcessSelectorRejectsIncompleteDescendantList(t *testing.T) {
	s, group := newProcessSelectorForTest(t)
	directory := s.source.memcgDir(group.Path)
	writeSubgroupProcesses(t, directory, "11\n")
	writeSubgroupProcesses(t, filepath.Join(directory, "child"), "invalid\n")
	writeProcessForTest(t, s.procRoot, memsnapshot.ProcessInstance{TGID: 11, StartTimeTicks: 100}, 100, 0)

	selected, err := s.Select(t.Context(), group, 1<<20)
	if err == nil || selected != (selectedProcess{}) {
		t.Fatalf("partial subtree selected %+v: %v", selected, err)
	}
}
