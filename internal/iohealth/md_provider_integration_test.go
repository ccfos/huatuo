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

//go:build linux

package iohealth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	mdIntegrationCommandTimeout = 15 * time.Second
	mdIntegrationEventTimeout   = 15 * time.Second
	mdIntegrationQuietPeriod    = 500 * time.Millisecond
	mdIntegrationImageSize      = 64 << 20
)

// Integration test (real MD RAID and loop devices):
//
//	TEST_INTEGRATION=true go test -v -run TestMDWatcherMonitorsMdadmLoopArray \
//	  ./internal/iohealth
func TestMDWatcherMonitorsMdadmLoopArray(t *testing.T) {
	mdadm, losetup := requireMDIntegrationEnvironment(t)

	workDir := t.TempDir()
	images := []string{
		filepath.Join(workDir, "member-0.img"),
		filepath.Join(workDir, "member-1.img"),
	}
	for _, image := range images {
		if err := os.WriteFile(image, nil, 0o600); err != nil {
			t.Fatalf("create loop backing file %s: %v", image, err)
		}
		if err := os.Truncate(image, mdIntegrationImageSize); err != nil {
			t.Fatalf("size loop backing file %s: %v", image, err)
		}
	}

	mdDevice := findUnusedMDIntegrationDevice(t)
	mdName := filepath.Base(mdDevice)
	mdSysfsPath := filepath.Join("/sys/block", mdName)

	var (
		loopDevices  []string
		arrayCreated bool
		watcher      *MDWatcher
		cancel       context.CancelFunc
	)
	t.Cleanup(func() {
		if cancel != nil {
			cancel()
			if err := watcher.Wait(); err != nil {
				t.Errorf("stop MD watcher: %v", err)
			}
		}

		if err := stopMDIntegrationArray(mdadm, mdDevice, mdSysfsPath, loopDevices); err != nil {
			t.Errorf("stop MD array during cleanup: %v", err)
		} else {
			waitForMDIntegrationPath(t, mdSysfsPath, false)
		}

		if arrayCreated {
			for _, loopDevice := range loopDevices {
				runMDIntegrationCleanup(t, mdadm,
					"--zero-superblock", "--force", loopDevice)
			}
		}
		for _, loopDevice := range loopDevices {
			runMDIntegrationCleanup(t, losetup, "--detach", loopDevice)
			waitForMDIntegrationLoopDetach(t, loopDevice)
		}
	})

	for _, image := range images {
		output := runMDIntegrationCommand(t, losetup, "--find", "--show", image)
		loopDevice := strings.TrimSpace(output)
		if !strings.HasPrefix(loopDevice, "/dev/loop") ||
			strings.Contains(loopDevice, "\n") {
			t.Fatalf("losetup returned invalid loop device %q", output)
		}
		loopDevices = append(loopDevices, loopDevice)
	}

	runMDIntegrationCommand(t, mdadm,
		"--create", mdDevice,
		"--metadata=1.2",
		"--level=1",
		"--raid-devices=2",
		"--assume-clean",
		"--run",
		"--force",
		loopDevices[0], loopDevices[1],
	)
	arrayCreated = true
	waitForMDIntegrationPath(t, mdSysfsPath, true)

	watcher = NewMDWatcher("/proc/mdstat", "/sys/block")
	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	if err := watcher.Start(ctx); err != nil {
		t.Fatalf("start MD watcher: %v", err)
	}

	assertNoMDIntegrationChange(t, watcher)

	failedMember := filepath.Base(loopDevices[1])
	runMDIntegrationCommand(t, mdadm, mdDevice, "--fail", loopDevices[1])
	waitForMDIntegrationFailure(t, watcher, mdName, failedMember)

	if err := stopMDIntegrationArray(mdadm, mdDevice, mdSysfsPath, loopDevices); err != nil {
		t.Fatalf("stop MD array: %v", err)
	}
	waitForMDIntegrationPath(t, mdSysfsPath, false)
}

func requireMDIntegrationEnvironment(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}
	if os.Geteuid() != 0 {
		t.Skip("MD integration test requires root privileges")
	}

	mdadm, err := exec.LookPath("mdadm")
	if err != nil {
		t.Skipf("MD integration test requires mdadm: %v", err)
	}
	losetup, err := exec.LookPath("losetup")
	if err != nil {
		t.Skipf("MD integration test requires losetup: %v", err)
	}
	if _, err := os.Stat("/proc/mdstat"); err != nil {
		t.Skipf("MD integration test requires /proc/mdstat: %v", err)
	}
	requireRAID1IntegrationPersonality(t)
	return mdadm, losetup
}

func requireRAID1IntegrationPersonality(t *testing.T) {
	t.Helper()
	mdstat, err := os.ReadFile("/proc/mdstat")
	if err != nil {
		t.Skipf("read /proc/mdstat: %v", err)
	}
	if strings.Contains(string(mdstat), "[raid1]") {
		return
	}

	modprobe, err := exec.LookPath("modprobe")
	if err != nil {
		t.Skip("RAID1 kernel personality is not loaded and modprobe is unavailable")
	}
	if output, err := executeMDIntegrationCommand(modprobe, "raid1"); err != nil {
		t.Skipf("RAID1 kernel personality is unavailable: %v; output: %s", err, output)
	}
	mdstat, err = os.ReadFile("/proc/mdstat")
	if err != nil || !strings.Contains(string(mdstat), "[raid1]") {
		t.Skip("modprobe completed but the RAID1 kernel personality is unavailable")
	}
}

func findUnusedMDIntegrationDevice(t *testing.T) string {
	t.Helper()
	for minor := 127; minor >= 0; minor-- {
		name := fmt.Sprintf("md%d", minor)
		if _, err := os.Stat(filepath.Join("/sys/block", name)); !os.IsNotExist(err) {
			continue
		}
		device := filepath.Join("/dev", name)
		info, err := os.Stat(device)
		if os.IsNotExist(err) || err == nil && info.Mode()&os.ModeDevice != 0 {
			return device
		}
	}
	t.Fatal("no unused /dev/md device is available")
	return ""
}

func waitForMDIntegrationFailure(
	t *testing.T,
	watcher *MDWatcher,
	array, member string,
) {
	t.Helper()
	timer := time.NewTimer(mdIntegrationEventTimeout)
	defer timer.Stop()

	memberFault := false
	degraded := false
	for {
		if degraded && memberFault {
			quietTimer := time.NewTimer(mdIntegrationQuietPeriod)
			defer quietTimer.Stop()
			select {
			case change := <-watcher.Changes():
				// mdadm --fail can move a RAID1 member through intermediate
				// states (in_sync -> blocked,faulty -> faulty) before it
				// settles, so an extra member transition is legitimate. Only a
				// no-op duplicate or a transition out of a failed state is
				// spurious.
				if change.Field == MDFieldMemberState && change.Member == member {
					if change.OldState == change.NewState {
						t.Fatalf("received duplicate MD member state notification: %+v", change)
					}
					if !mdIntegrationStateContains(change.NewState, "faulty") &&
						!mdIntegrationStateContains(change.NewState, "blocked") {
						t.Fatalf("MD member %s left its failed state: %+v", member, change)
					}
				}
				continue
			case <-quietTimer.C:
				return
			}
		}

		select {
		case change := <-watcher.Changes():
			switch change.Field {
			case MDFieldDegraded:
				if change.Array == array && change.NewState == "1" {
					degraded = true
				}
			case MDFieldMemberState:
				if change.Member == member {
					if change.OldState == change.NewState {
						t.Fatalf("received duplicate MD member state notification: %+v", change)
					}
					if !memberFault {
						assertMDIntegrationFailureChange(t, &change, array, member)
						memberFault = true
					}
				}
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s/%s fault and degraded=1",
				array, member)
		}
	}
}

func assertMDIntegrationFailureChange(
	t *testing.T,
	change *MDChange,
	array, member string,
) {
	t.Helper()
	if change.Array != array || change.Member != member ||
		change.Field != MDFieldMemberState ||
		!mdIntegrationStateContains(change.OldState, "in_sync") ||
		!mdIntegrationStateContains(change.NewState, "faulty") ||
		change.ObservedAt.IsZero() {
		t.Fatalf("MD member change = %+v, want %s/%s in_sync -> faulty",
			change, array, member)
	}
}

func mdIntegrationStateContains(state, target string) bool {
	for _, value := range strings.Split(state, ",") {
		if value == target {
			return true
		}
	}
	return false
}

func assertNoMDIntegrationChange(t *testing.T, watcher *MDWatcher) {
	t.Helper()
	select {
	case change := <-watcher.Changes():
		t.Fatalf("unexpected initial MD change: %+v", change)
	default:
	}
}

func waitForMDIntegrationPath(t *testing.T, path string, wantExists bool) {
	t.Helper()
	deadline := time.Now().Add(mdIntegrationEventTimeout)
	for {
		_, err := os.Stat(path)
		exists := err == nil
		if exists == wantExists {
			return
		}
		if err != nil && !os.IsNotExist(err) {
			t.Errorf("stat %s: %v", path, err)
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("timed out waiting for %s existence=%t", path, wantExists)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runMDIntegrationCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	output, err := executeMDIntegrationCommand(name, args...)
	if err != nil {
		t.Fatalf("%v; output: %s", err, output)
	}
	return output
}

func runMDIntegrationCleanup(t *testing.T, name string, args ...string) {
	t.Helper()
	output, err := executeMDIntegrationCommand(name, args...)
	if err != nil {
		t.Errorf("MD integration cleanup: %v; output: %s", err, output)
	}
}

func stopMDIntegrationArray(mdadm, device, sysfsPath string, loopDevices []string) error {
	if _, err := os.Stat(sysfsPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	// A free name is not a reservation. Check the actual members even when
	// create failed: it may have bound only a subset of our loop devices.
	entries, err := os.ReadDir(filepath.Join(sysfsPath, "md"))
	if err != nil {
		return fmt.Errorf("read %s members before stop: %w", device, err)
	}
	hasMember := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "dev-") {
			continue
		}
		owned := false
		for _, loopDevice := range loopDevices {
			if entry.Name() == "dev-"+filepath.Base(loopDevice) {
				owned = true
				break
			}
		}
		if !owned {
			return fmt.Errorf("refuse to stop %s: member %s is not owned by this test", device, entry.Name())
		}
		hasMember = true
	}
	if !hasMember {
		return fmt.Errorf("refuse to stop %s: no members confirm test ownership", device)
	}
	_, err = executeMDIntegrationCommand(mdadm, "--stop", "--force", device)
	return err
}

func TestMDIntegrationCleanupAfterCreateFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		members string
		wantErr bool
	}{
		{name: "foreign array", members: "loop99", wantErr: true},
		{name: "mixed members", members: "loop20 loop99", wantErr: true},
		{name: "empty array", wantErr: true},
		{name: "own array", members: "loop20 loop21"},
		{name: "partial create", members: "loop20"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			sysfsPath := filepath.Join(root, "md127")
			callsPath := filepath.Join(root, "stops")
			mdadm := filepath.Join(root, "mdadm")
			// The command creates the observable member bindings, then fails
			// before the caller can record a successful create.
			const command = `#!/bin/sh
set -eu
case "$1" in
--create)
	mkdir -p "$MD_TEST_SYSFS/md"
	for member in $MD_TEST_MEMBERS; do mkdir "$MD_TEST_SYSFS/md/dev-$member"; done
	echo "injected create failure" >&2
	exit 1
	;;
--stop)
	printf '%s\n' "$*" >> "$MD_TEST_STOPS"
	for member in "$MD_TEST_SYSFS"/md/dev-*; do
		[ ! -d "$member" ] || rmdir "$member"
	done
	rmdir "$MD_TEST_SYSFS/md" "$MD_TEST_SYSFS"
	;;
*) exit 2 ;;
esac
`
			if err := os.WriteFile(mdadm, []byte(command), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(mdadm, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MD_TEST_SYSFS", sysfsPath)
			t.Setenv("MD_TEST_STOPS", callsPath)
			t.Setenv("MD_TEST_MEMBERS", test.members)
			var cleanupErr error
			func() {
				defer func() {
					cleanupErr = stopMDIntegrationArray(mdadm, "/dev/md127", sysfsPath,
						[]string{"/dev/loop20", "/dev/loop21"})
				}()
				output, err := executeMDIntegrationCommand(mdadm, "--create", "/dev/md127")
				if err == nil || !strings.Contains(output, "injected create failure") {
					t.Fatalf("create result = (%q, %v), want injected command failure", output, err)
				}
			}()
			if (cleanupErr != nil) != test.wantErr {
				t.Errorf("cleanup error = %v, want error %t", cleanupErr, test.wantErr)
			}
			calls, err := os.ReadFile(callsPath)
			if test.wantErr {
				if !os.IsNotExist(err) {
					t.Fatalf("unowned array received stop: %q, read error %v", calls, err)
				}
				if _, err := os.Stat(sysfsPath); err != nil {
					t.Fatalf("unowned array was removed: %v", err)
				}
			} else if err != nil || string(calls) != "--stop --force /dev/md127\n" {
				t.Fatalf("owned array stop = %q, read error %v", calls, err)
			}
		})
	}
}

func TestMDIntegrationCleanupRequiresReadableMembers(t *testing.T) {
	root := t.TempDir()
	// A non-directory makes ReadDir fail even when the test runs as root.
	if err := os.WriteFile(filepath.Join(root, "md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := stopMDIntegrationArray(filepath.Join(root, "must-not-run"), "/dev/md127", root,
		[]string{"/dev/loop20"})
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("cleanup error = %v, want member lookup failure before running mdadm", err)
	}
}

func waitForMDIntegrationLoopDetach(t *testing.T, loopDevice string) {
	t.Helper()
	backingFile := filepath.Join(
		"/sys/block", filepath.Base(loopDevice), "loop", "backing_file",
	)
	waitForMDIntegrationPath(t, backingFile, false)
}

func executeMDIntegrationCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mdIntegrationCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		return strings.TrimSpace(string(output)),
			fmt.Errorf("command %s timed out: %w", name, ctx.Err())
	}
	if err != nil {
		return strings.TrimSpace(string(output)),
			fmt.Errorf("command %s %s failed: %w",
				name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}
