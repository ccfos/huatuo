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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func newProcessSelectorForTest(t *testing.T) (*processSelector, cgroupRef) {
	t.Helper()
	source := &cgroupSource{root: t.TempDir()}
	if err := os.Mkdir(source.memcgDir("/processes"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &processSelector{source: source, procRoot: t.TempDir()}, cgroupRefForTest(t, source, "/processes")
}

func writeProcessForTest(t *testing.T, root string, identity memsnapshot.ProcessIdentity, rssKiB uint64, adj int) {
	t.Helper()
	directory := filepath.Join(root, strconv.Itoa(identity.TGID))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.Repeat("0 ", 52))
	fields[0], fields[1], fields[2] = strconv.Itoa(identity.TGID), "(worker)", "S"
	fields[21] = strconv.FormatUint(identity.StartTimeTicks, 10)
	writeMemoryEventsForTest(t, filepath.Join(directory, "stat"), strings.Join(fields, " "))
	writeMemoryEventsForTest(t, filepath.Join(directory, "status"), fmt.Sprintf("Name:\tworker\nVmRSS:\t%d kB\nVmSwap:\t0 kB\nVmPTE:\t4 kB\n", rssKiB))
	writeMemoryEventsForTest(t, filepath.Join(directory, "oom_score_adj"), strconv.Itoa(adj))
}

func TestProcessSelectorSelect(t *testing.T) {
	s, group := newProcessSelectorForTest(t)
	writeProcessForTest(t, s.procRoot, memsnapshot.ProcessIdentity{TGID: 11, StartTimeTicks: 100}, 100, 0)
	writeProcessForTest(t, s.procRoot, memsnapshot.ProcessIdentity{TGID: 22, StartTimeTicks: 200}, 200, 0)
	writeProcessForTest(t, s.procRoot, memsnapshot.ProcessIdentity{TGID: 33, StartTimeTicks: 300}, 300, -1000)
	writeMemoryEventsForTest(t, filepath.Join(s.source.memcgDir(group.Path), "cgroup.procs"), "11\n22\n33\n44\n")
	process, err := s.Select(t.Context(), group, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	want := selectedProcess{identity: memsnapshot.ProcessIdentity{TGID: 22, StartTimeTicks: 200}, comm: "worker"}
	if process != want {
		t.Fatalf("selected process = %+v, want %+v", process, want)
	}
}

func TestSelectProcessRequiresCompleteEnumeration(t *testing.T) {
	failure := errors.New("process list read failed")
	for _, reason := range []error{os.ErrNotExist, unix.ESRCH, errProcessNotEligible, os.ErrPermission, failure} {
		t.Run(reason.Error(), func(t *testing.T) {
			process, err := selectProcessFromProcs(func(visit func(int) error) error {
				if err := visit(11); err != nil {
					return err
				}
				if errors.Is(reason, failure) {
					return failure
				}
				return visit(22)
			}, func(pid int) (processCandidate, error) {
				if pid == 22 {
					return processCandidate{}, reason
				}
				return processCandidate{process: selectedProcess{
					identity: memsnapshot.ProcessIdentity{TGID: pid, StartTimeTicks: 100},
				}}, nil
			})
			if errors.Is(reason, failure) || errors.Is(reason, os.ErrPermission) {
				if !errors.Is(err, reason) || process != (selectedProcess{}) {
					t.Fatalf("partial enumeration selected %+v: %v", process, err)
				}
				return
			}
			if err != nil || process.identity.TGID != 11 {
				t.Fatalf("ineligible process prevented selection: %+v, %v", process, err)
			}
		})
	}
}

func TestProcessSelectorNoEligibleProcess(t *testing.T) {
	for _, members := range []string{"", "11\n", "22\n"} {
		s, group := newProcessSelectorForTest(t)
		writeProcessForTest(t, s.procRoot, memsnapshot.ProcessIdentity{TGID: 11, StartTimeTicks: 100}, 100, -1000)
		writeMemoryEventsForTest(t, filepath.Join(s.source.memcgDir(group.Path), "cgroup.procs"), members)
		process, err := s.Select(t.Context(), group, 1<<20)
		if !errors.Is(err, errNoSnapshotProcess) || process != (selectedProcess{}) {
			t.Fatalf("members %q: process=%+v, err=%v", members, process, err)
		}
	}
}

func TestProcessSelectorRejectsStaleBinding(t *testing.T) {
	for _, change := range []string{"unchanged", "moved", "reused", "directory replaced", "canceled", "deadline"} {
		t.Run(change, func(t *testing.T) {
			s, group := newProcessSelectorForTest(t)
			identity := memsnapshot.ProcessIdentity{TGID: 11, StartTimeTicks: 100}
			writeProcessForTest(t, s.procRoot, identity, 100, 0)
			members := filepath.Join(s.source.memcgDir(group.Path), "cgroup.procs")
			writeMemoryEventsForTest(t, members, "11\n")
			ctx := t.Context()
			wantErr := errInvalidSnapshotProcess
			switch change {
			case "unchanged":
				wantErr = nil
			case "moved":
				writeMemoryEventsForTest(t, members, "22\n")
			case "reused":
				writeProcessForTest(t, s.procRoot, memsnapshot.ProcessIdentity{TGID: 11, StartTimeTicks: 101}, 100, 0)
			case "directory replaced":
				if err := os.Rename(s.source.memcgDir(group.Path), s.source.memcgDir("/old")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(s.source.memcgDir(group.Path), 0o700); err != nil {
					t.Fatal(err)
				}
				writeMemoryEventsForTest(t, members, "11\n")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				wantErr = context.Canceled
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				wantErr = context.DeadlineExceeded
			}
			if err := s.Validate(ctx, group, identity); !errors.Is(err, wantErr) {
				t.Fatalf("validation = %v, want %v", err, wantErr)
			}
			if change == "directory replaced" || change == "canceled" || change == "deadline" {
				process, err := s.Select(ctx, group, 1<<20)
				if !errors.Is(err, wantErr) || process != (selectedProcess{}) {
					t.Fatalf("invalid selection: process=%+v err=%v", process, err)
				}
			}
		})
	}
}

func TestProcessSelectorRejectsReplacementDuringEnumeration(t *testing.T) {
	s, group := newProcessSelectorForTest(t)
	writeMemoryEventsForTest(t, filepath.Join(s.source.memcgDir(group.Path), "cgroup.procs"), "11\n")
	err := s.scanProcesses(t.Context(), group, func(int) error {
		if err := os.Rename(s.source.memcgDir(group.Path), s.source.memcgDir("/old")); err != nil {
			return err
		}
		return os.Mkdir(s.source.memcgDir(group.Path), 0o700)
	})
	if !errors.Is(err, errInvalidSnapshotProcess) {
		t.Fatalf("directory replacement during enumeration accepted: %v", err)
	}
}

func TestScanProcessPIDs(t *testing.T) {
	for _, test := range []struct {
		name, input string
		wantCount   int
		wantErr     bool
	}{
		{name: "empty"},
		{name: "valid", input: "1\n22\n333\n", wantCount: 3},
		{name: "pid limit", input: strings.Repeat("1\n", maxCgroupProcesses), wantCount: maxCgroupProcesses},
		{name: "pid overflow", input: strings.Repeat("1\n", maxCgroupProcesses+1), wantErr: true},
		{name: "byte overflow", input: strings.Repeat("00000000000000001\n", maxCgroupProcesses), wantErr: true},
		{name: "invalid", input: "1\ninvalid\n", wantErr: true},
		{name: "nonpositive", input: "0\n", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			count := 0
			err := scanProcessPIDs(t.Context(), strings.NewReader(test.input), func(int) error { count++; return nil })
			if (err != nil) != test.wantErr || (!test.wantErr && count != test.wantCount) {
				t.Fatalf("count=%d err=%v, want count=%d failure=%t", count, err, test.wantCount, test.wantErr)
			}
		})
	}
}
