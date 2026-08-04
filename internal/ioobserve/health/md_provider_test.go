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

package health

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type mdFixture struct {
	mdstat   string
	sysBlock string
}

type mdPollResult struct {
	fd     int32
	events int16
	err    error
}

type fakeMDPoller struct {
	results chan mdPollResult
}

func newFakeMDPoller() *fakeMDPoller {
	return &fakeMDPoller{results: make(chan mdPollResult, 16)}
}

func (p *fakeMDPoller) poll(fds []unix.PollFd, _ int) (int, error) {
	result := <-p.results
	if result.err != nil {
		return 0, result.err
	}
	for i := range fds {
		if fds[i].Fd == result.fd {
			fds[i].Revents = result.events
			return 1, nil
		}
	}
	return 0, unix.EINTR
}

func (p *fakeMDPoller) trigger(fd int32) {
	p.results <- mdPollResult{fd: fd, events: unix.POLLPRI | unix.POLLERR}
}

func (p *fakeMDPoller) fail(err error) {
	p.results <- mdPollResult{err: err}
}

func setupMDFixture(t *testing.T, devices map[string]map[string]string) mdFixture {
	t.Helper()
	root := t.TempDir()
	mdstat := filepath.Join(root, "proc", "mdstat")
	sysBlock := filepath.Join(root, "sys", "block")
	if err := os.MkdirAll(filepath.Dir(mdstat), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sysBlock, 0o755); err != nil {
		t.Fatal(err)
	}
	for device, files := range devices {
		writeMDDevice(t, sysBlock, device, files)
	}
	deviceNames := make([]string, 0, len(devices))
	for device := range devices {
		deviceNames = append(deviceNames, device)
	}
	writeMDStatFixture(t, mdstat, deviceNames...)
	return mdFixture{mdstat: mdstat, sysBlock: sysBlock}
}

func writeMDStatFixture(t *testing.T, path string, devices ...string) {
	writeMDStatFixtureForLevel(t, path, "raid1", devices...)
}

func writeMDStatFixtureForLevel(
	t *testing.T,
	path, level string,
	devices ...string,
) {
	t.Helper()
	sort.Strings(devices)
	var content strings.Builder
	fmt.Fprintf(&content, "Personalities : [%s]\n", level)
	for _, device := range devices {
		fmt.Fprintf(&content, "%s : active %s\n", device, level)
	}
	content.WriteString("unused devices: <none>\n")
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMDDevice(t *testing.T, sysBlock, device string, files map[string]string) {
	t.Helper()
	mdDir := filepath.Join(sysBlock, device, "md")
	for name, content := range files {
		path := filepath.Join(mdDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func startMDWatcher(t *testing.T, fixture mdFixture) (*MDWatcher, *fakeMDPoller, context.CancelFunc) {
	t.Helper()
	poller := newFakeMDPoller()
	watcher := newMDWatcher(fixture.mdstat, fixture.sysBlock, poller.poll)
	ctx, cancel := context.WithCancel(context.Background())
	if err := watcher.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start() error = %v", err)
	}
	return watcher, poller, cancel
}

func stopMDWatcher(
	t *testing.T,
	watcher *MDWatcher,
	poller *fakeMDPoller,
	cancel context.CancelFunc,
) {
	t.Helper()
	watcher.lifecycleMu.Lock()
	wakeRead := watcher.wakeRead
	watcher.lifecycleMu.Unlock()
	cancel()
	poller.trigger(int32(wakeRead))
	watcher.Wait()
}

func mdFileFD(t *testing.T, watcher *MDWatcher, array, member, field string) int32 {
	t.Helper()
	return int32(mdFileTarget(t, watcher, array, member, field).file.Fd())
}

func mdFileTarget(
	t *testing.T,
	watcher *MDWatcher,
	array, member, field string,
) *mdWatchFile {
	t.Helper()
	watcher.mu.RLock()
	defer watcher.mu.RUnlock()
	for _, target := range watcher.files {
		if target.array == array && target.member == member && target.field == field {
			return target
		}
	}
	t.Fatalf("watch %s/%s/%s not found", array, member, field)
	return nil
}

func waitMDChange(t *testing.T, watcher *MDWatcher, match func(MDChange) bool) MDChange {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case change := <-watcher.Changes():
			if match(change) {
				return change
			}
		case <-deadline:
			t.Fatal("timed out waiting for MD change")
		}
	}
}

func assertNoMDChange(t *testing.T, watcher *MDWatcher) {
	t.Helper()
	select {
	case change := <-watcher.Changes():
		t.Fatalf("unexpected MD change: %+v", change)
	default:
	}
}

func assertMDMemberWatchPreserved(
	t *testing.T,
	watcher *MDWatcher,
	previous *mdWatchFile,
	array, member, state string,
) {
	t.Helper()

	current := mdFileTarget(t, watcher, array, member, mdMemberState)
	if current != previous {
		t.Fatalf("%s/%s watch was replaced after failed rebuild", array, member)
	}
	watcher.mu.RLock()
	currentState := watcher.members[array][member]
	watcher.mu.RUnlock()
	if currentState != state {
		t.Fatalf("%s/%s state = %q, want %q", array, member, currentState, state)
	}
	value, err := readOpenMDFile(current.file)
	if err != nil {
		t.Fatalf("preserved %s/%s watch cannot be read: %v", array, member, err)
	}
	if value != state {
		t.Fatalf("preserved %s/%s watch value = %q, want %q", array, member, value, state)
	}
	assertNoMDChange(t, watcher)
}

func TestMDWatcherReportsTransitionsAfterInitialBaseline(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
			"dev-sdb/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	assertNoMDChange(t, watcher)

	syncPath := filepath.Join(fixture.sysBlock, "md0", "md", mdSyncAction)
	if err := os.WriteFile(syncPath, []byte("recover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "", mdSyncAction))
	change := waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldSyncAction
	})
	if change.Array != "md0" || change.Member != "" ||
		change.OldState != "idle" || change.NewState != "recover" ||
		change.ObservedAt.IsZero() {
		t.Fatalf("sync change = %+v", change)
	}

	memberPath := filepath.Join(fixture.sysBlock, "md0", "md", "dev-sdb", mdMemberState)
	if err := os.WriteFile(memberPath, []byte("faulty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "sdb", mdMemberState))
	change = waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldMemberState
	})
	if change.Array != "md0" || change.Member != "sdb" ||
		change.OldState != "in_sync" || change.NewState != "faulty" ||
		change.ObservedAt.IsZero() {
		t.Fatalf("member change = %+v", change)
	}

	degradedPath := filepath.Join(fixture.sysBlock, "md0", "md", mdDegraded)
	if err := os.WriteFile(degradedPath, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "", mdDegraded))
	change = waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldDegraded
	})
	if change.Array != "md0" || change.OldState != "0" ||
		change.NewState != "1" {
		t.Fatalf("degraded change = %+v", change)
	}
	assertNoMDChange(t, watcher)
}

func TestMDWatcherStartsWithoutRedundancyAttributes(t *testing.T) {
	for _, level := range []string{"raid0", "linear"} {
		t.Run(level, func(t *testing.T) {
			fixture := setupMDFixture(t, map[string]map[string]string{
				"md0": {
					mdLevel:                    level + "\n",
					"dev-sda/" + mdMemberState: "in_sync\n",
				},
			})
			writeMDStatFixtureForLevel(t, fixture.mdstat, level, "md0")
			watcher, poller, cancel := startMDWatcher(t, fixture)
			defer stopMDWatcher(t, watcher, poller, cancel)

			statePath := filepath.Join(
				fixture.sysBlock,
				"md0",
				"md",
				"dev-sda",
				mdMemberState,
			)
			if err := os.WriteFile(statePath, []byte("faulty\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			poller.trigger(mdFileFD(t, watcher, "md0", "sda", mdMemberState))

			change := waitMDChange(t, watcher, func(change MDChange) bool {
				return change.Field == MDFieldMemberState
			})
			if change.Array != "md0" || change.Member != "sda" ||
				change.OldState != "in_sync" || change.NewState != "faulty" {
				t.Fatalf("member change = %+v", change)
			}
		})
	}
}

func TestMDWatcherStopsAfterNotificationRebuildFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		array string
		field string
	}{
		{name: "mdstat", field: mdStatField},
		{name: "level", array: "md0", field: mdLevel},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupMDFixture(t, map[string]map[string]string{
				"md0": {
					mdLevel:                    "raid1\n",
					mdSyncAction:               "idle\n",
					mdDegraded:                 "0\n",
					"dev-sda/" + mdMemberState: "in_sync\n",
				},
			})
			watcher, poller, cancel := startMDWatcher(t, fixture)
			defer stopMDWatcher(t, watcher, poller, cancel)

			statePath := filepath.Join(
				fixture.sysBlock,
				"md0",
				"md",
				"dev-sda",
				mdMemberState,
			)
			if err := os.Remove(statePath); err != nil {
				t.Fatal(err)
			}
			poller.trigger(mdFileFD(t, watcher, test.array, "", test.field))
			sentinel := errors.New("unexpected second poll")
			poller.fail(sentinel)

			err := watcher.Wait()
			if err == nil {
				t.Fatal("Wait() error = nil, want rebuild error")
			}
			if errors.Is(err, sentinel) {
				t.Fatalf("Wait() error = %v, watcher continued polling", err)
			}
			if !strings.Contains(err.Error(), "rebuild after "+test.field+" notification") {
				t.Fatalf("Wait() error = %v, want notification rebuild context", err)
			}
		})
	}
}

func TestMDWatcherPollsOnlyForPriorityChanges(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:      "raid1\n",
			mdSyncAction: "idle\n",
			mdDegraded:   "0\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	watcher.lifecycleMu.Lock()
	wakeRead := watcher.wakeRead
	watcher.lifecycleMu.Unlock()
	pollFDs, targets := watcher.pollFDs(wakeRead)
	if len(pollFDs) != len(targets) || len(pollFDs) < 2 {
		t.Fatalf("poll set has %d fds and %d targets", len(pollFDs), len(targets))
	}
	if pollFDs[0].Events != unix.POLLIN {
		t.Fatalf("wake pipe events = %#x, want POLLIN", pollFDs[0].Events)
	}
	for i := 1; i < len(pollFDs); i++ {
		if pollFDs[i].Events != unix.POLLPRI {
			t.Fatalf(
				"watch %s events = %#x, want POLLPRI",
				targets[i].field,
				pollFDs[i].Events,
			)
		}
	}
}

func TestMDWatcherRebuildPreservesMemberFaultNotification(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
			"dev-sdb/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	mdstatTarget := mdFileTarget(t, watcher, "", "", mdStatField)
	memberTarget := mdFileTarget(t, watcher, "md0", "sdb", mdMemberState)
	memberPath := filepath.Join(fixture.sysBlock, "md0", "md", "dev-sdb", mdMemberState)
	if err := os.WriteFile(memberPath, []byte("faulty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A real fault can make mdstat and the member fd ready together. Model
	// mdstat being handled first: rebuild replaces and closes memberTarget,
	// so the state diff must preserve the notification before the stale fd is
	// ignored.
	watcher.handleEvent(mdstatTarget, unix.POLLPRI|unix.POLLERR)
	watcher.handleEvent(memberTarget, unix.POLLPRI|unix.POLLERR)

	select {
	case change := <-watcher.Changes():
		if change.Array != "md0" || change.Member != "sdb" ||
			change.Field != MDFieldMemberState ||
			change.OldState != "in_sync" || change.NewState != "faulty" {
			t.Fatalf("member change = %+v", change)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rebuilt member change")
	}
	assertNoMDChange(t, watcher)
}

func TestMDWatcherEmitsEveryMemberFaultSeenWithDegradedChange(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
			"dev-sdb/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	degradedTarget := mdFileTarget(t, watcher, "md0", "", mdDegraded)
	memberTargets := []*mdWatchFile{
		mdFileTarget(t, watcher, "md0", "sda", mdMemberState),
		mdFileTarget(t, watcher, "md0", "sdb", mdMemberState),
	}
	mdDir := filepath.Join(fixture.sysBlock, "md0", "md")
	for _, member := range []string{"sda", "sdb"} {
		if err := os.WriteFile(
			filepath.Join(mdDir, "dev-"+member, mdMemberState),
			[]byte("faulty\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mdDir, mdDegraded), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	watcher.handleEvent(degradedTarget, unix.POLLPRI|unix.POLLERR)
	degraded := waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldDegraded
	})
	if degraded.OldState != "0" || degraded.NewState != "2" {
		t.Fatalf("degraded change = %+v", degraded)
	}
	changes := make(map[string]MDChange)
	for range 2 {
		select {
		case change := <-watcher.Changes():
			changes[change.Member] = change
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for both faulted member changes")
		}
	}
	for _, member := range []string{"sda", "sdb"} {
		change, ok := changes[member]
		if !ok || change.Array != "md0" ||
			change.Field != MDFieldMemberState ||
			change.OldState != "in_sync" || change.NewState != "faulty" {
			t.Fatalf("%s change = %+v, present=%t", member, change, ok)
		}
	}

	// Member fds can already be ready when degraded is processed. Their stale
	// notifications must not duplicate changes whose cache was refreshed above.
	for _, target := range memberTargets {
		watcher.handleEvent(target, unix.POLLPRI|unix.POLLERR)
	}
	assertNoMDChange(t, watcher)
}

func TestMDWatcherReportsRemovedMemberFromActiveArray(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
			"dev-sdb/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	mdstatTarget := mdFileTarget(t, watcher, "", "", mdStatField)
	mdDir := filepath.Join(fixture.sysBlock, "md0", "md")
	if err := os.RemoveAll(filepath.Join(mdDir, "dev-sdb")); err != nil {
		t.Fatal(err)
	}
	watcher.handleEvent(mdstatTarget, unix.POLLPRI|unix.POLLERR)

	select {
	case change := <-watcher.Changes():
		if change.Array != "md0" || change.Member != "sdb" ||
			change.Field != MDFieldMemberState ||
			change.OldState != "in_sync" ||
			change.NewState != MDMemberStateRemoved {
			t.Fatalf("removed member change = %+v", change)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for removed member change")
	}

	// Adding a replacement or expansion member is not a health failure.
	writeMDDevice(t, fixture.sysBlock, "md0", map[string]string{
		"dev-sdc/" + mdMemberState: "spare\n",
	})
	mdstatTarget = mdFileTarget(t, watcher, "", "", mdStatField)
	watcher.handleEvent(mdstatTarget, unix.POLLPRI|unix.POLLERR)
	assertNoMDChange(t, watcher)
}

func TestMDWatcherReportsMemberWriteError(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	statePath := filepath.Join(
		fixture.sysBlock,
		"md0",
		"md",
		"dev-sda",
		mdMemberState,
	)
	if err := os.WriteFile(statePath, []byte("in_sync,write_error\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "sda", mdMemberState))

	change := waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldMemberState
	})
	if change.Member != "sda" || change.OldState != "in_sync" ||
		change.NewState != "in_sync,write_error" {
		t.Fatalf("write_error change = %+v", change)
	}
}

func TestMDWatcherRejectsIncompleteArrayBaseline(t *testing.T) {
	tests := []struct {
		name       string
		field      string
		breakField func(*testing.T, string)
	}{
		{
			name:  "unreadable sync action",
			field: mdSyncAction,
			breakField: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "unreadable level",
			field: mdLevel,
			breakField: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "malformed degraded",
			field: mdDegraded,
			breakField: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("invalid\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupMDFixture(t, map[string]map[string]string{
				"md0": {
					mdLevel:      "raid1\n",
					mdSyncAction: "idle\n",
					mdDegraded:   "0\n",
				},
			})
			path := filepath.Join(fixture.sysBlock, "md0", "md", test.field)
			test.breakField(t, path)

			poller := newFakeMDPoller()
			watcher := newMDWatcher(fixture.mdstat, fixture.sysBlock, poller.poll)
			ctx, cancel := context.WithCancel(context.Background())
			err := watcher.Start(ctx)
			if err == nil {
				stopMDWatcher(t, watcher, poller, cancel)
				t.Fatal("Start() error = nil, want incomplete baseline error")
			}
			cancel()
		})
	}
}

func TestMDWatcherRejectsIncompleteMemberStateRebuild(t *testing.T) {
	tests := []struct {
		name       string
		breakState func(*testing.T, string)
	}{
		{
			name: "missing state",
			breakState: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unreadable state",
			breakState: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupMDFixture(t, map[string]map[string]string{
				"md0": {
					mdLevel:                    "raid1\n",
					mdSyncAction:               "idle\n",
					mdDegraded:                 "0\n",
					"dev-sda/" + mdMemberState: "in_sync\n",
					"dev-sdb/" + mdMemberState: "in_sync\n",
				},
			})
			watcher, poller, cancel := startMDWatcher(t, fixture)
			defer stopMDWatcher(t, watcher, poller, cancel)

			previous := mdFileTarget(t, watcher, "md0", "sdb", mdMemberState)
			statePath := filepath.Join(
				fixture.sysBlock,
				"md0",
				"md",
				"dev-sdb",
				mdMemberState,
			)
			test.breakState(t, statePath)

			watcher.mu.Lock()
			err := watcher.rebuildLocked(true)
			watcher.mu.Unlock()
			if err == nil {
				t.Fatal("rebuildLocked() error = nil, want member state error")
			}
			assertMDMemberWatchPreserved(
				t,
				watcher,
				previous,
				"md0",
				"sdb",
				"in_sync",
			)
		})
	}
}

func TestMDWatcherRejectsIncompleteMemberEnumeration(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:                    "raid1\n",
			mdSyncAction:               "idle\n",
			mdDegraded:                 "0\n",
			"dev-sda/" + mdMemberState: "in_sync\n",
			"dev-sdb/" + mdMemberState: "in_sync\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	previous := mdFileTarget(t, watcher, "md0", "sdb", mdMemberState)
	mdDir := filepath.Join(fixture.sysBlock, "md0", "md")
	if err := os.RemoveAll(mdDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	watcher.mu.Lock()
	err := watcher.rebuildLocked(true)
	watcher.mu.Unlock()
	if err == nil {
		t.Fatal("rebuildLocked() error = nil, want member enumeration error")
	}
	assertMDMemberWatchPreserved(t, watcher, previous, "md0", "sdb", "in_sync")
}

func TestMDWatcherReportsArrayChangeDuringTopologyRebuild(t *testing.T) {
	fixture := setupMDFixture(t, map[string]map[string]string{
		"md0": {
			mdLevel:      "raid1\n",
			mdSyncAction: "idle\n",
			mdDegraded:   "0\n",
		},
	})
	watcher, poller, cancel := startMDWatcher(t, fixture)
	defer stopMDWatcher(t, watcher, poller, cancel)

	levelPath := filepath.Join(fixture.sysBlock, "md0", "md", mdLevel)
	syncPath := filepath.Join(fixture.sysBlock, "md0", "md", mdSyncAction)
	if err := os.WriteFile(levelPath, []byte("raid10\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(syncPath, []byte("reshape\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	poller.trigger(mdFileFD(t, watcher, "md0", "", mdLevel))
	change := waitMDChange(t, watcher, func(change MDChange) bool {
		return change.Field == MDFieldSyncAction
	})
	if change.Array != "md0" || change.OldState != "idle" ||
		change.NewState != "reshape" {
		t.Fatalf("sync change = %+v", change)
	}

	writeMDDevice(t, fixture.sysBlock, "md1", map[string]string{
		mdLevel:      "raid1\n",
		mdSyncAction: "resync\n",
		mdDegraded:   "1\n",
	})
	writeMDStatFixture(t, fixture.mdstat, "md0", "md1")
	watcher.handleEvent(
		mdFileTarget(t, watcher, "", "", mdStatField),
		unix.POLLPRI|unix.POLLERR,
	)
	_ = mdFileTarget(t, watcher, "md1", "", mdSyncAction)
	assertNoMDChange(t, watcher)

	writeMDStatFixture(t, fixture.mdstat, "md1")
	watcher.handleEvent(
		mdFileTarget(t, watcher, "", "", mdStatField),
		unix.POLLPRI|unix.POLLERR,
	)
	assertNoMDChange(t, watcher)
}

func TestNormalizeMDSyncAction(t *testing.T) {
	for input, want := range map[string]string{
		"recover\n": "recover",
		"FROZEN":    "frozen",
		"reserved":  "unknown",
		"":          "unknown",
	} {
		if got := normalizeMDSyncAction(input); got != want {
			t.Fatalf("normalizeMDSyncAction(%q) = %q, want %q", input, got, want)
		}
	}
}
