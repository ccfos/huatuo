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

package pod

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
)

const (
	guestSidecarID  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	guestCheckoutID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func guestFile(t testing.TB, root, name, content string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func guestProc(t testing.TB, s *guestScanner, pid, parent int, start uint64, membership string) {
	t.Helper()
	name := strconv.Itoa(pid)
	guestFile(t, s.options.ProcRoot, filepath.Join(name, "cgroup"), membership)
	var stat strings.Builder
	fmt.Fprintf(&stat, "%d (checkout worker) S %d", pid, parent)
	for field := 5; field <= 52; field++ {
		if field == 22 {
			fmt.Fprintf(&stat, " %d", start)
		} else {
			stat.WriteString(" 0")
		}
	}
	guestFile(t, s.options.ProcRoot, filepath.Join(name, "stat"), stat.String()+"\n")
}

func guestFixture(t testing.TB) *guestScanner {
	t.Helper()
	s := &guestScanner{options: GuestScannerOptions{ProcRoot: t.TempDir(), CgroupRoot: t.TempDir(), PodName: "checkout-api", PodNamespace: "payments"}}
	guestFile(t, s.options.ProcRoot, "self/cgroup", "0::/"+guestSidecarID+"\n")
	return s
}

func guestDirectory(t testing.TB, root, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, path), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestInitGuestManagerKeepsDiscoveryWithoutCSS(t *testing.T) {
	scanner := guestFixture(t)
	previousView := containerView
	previousLoader := cgroupSubSysLoader
	previousIDs := cgroupCssID2SubSysNameMap
	containerView = newTestContainerStore()
	cgroupCssID2SubSysNameMap = map[int]string{}
	attempted := make(chan struct{}, 1)
	cgroupSubSysLoader = func() error {
		attempted <- struct{}{}
		return errors.New("Guest kernel has no BTF")
	}
	t.Cleanup(func() {
		ReleaseManager()
		containerView = previousView
		cgroupSubSysLoader = previousLoader
		cgroupCssID2SubSysNameMap = previousIDs
	})
	if err := InitGuestManager(scanner.options); err != nil {
		t.Fatal(err)
	}
	sub := subscribeForTest(t, containerView)
	waitContainerViewForTest(t, sub, 0)
	select {
	case <-attempted:
	default:
		t.Error("Guest manager did not initialize the cgroup CSS producer")
	}
}

func TestGuestScannerDiscovery(t *testing.T) {
	s := guestFixture(t)
	path := "/kubepods/burstable/" + guestCheckoutID
	guestDirectory(t, s.options.CgroupRoot, path)
	guestProc(t, s, 150, 1, 100, "0::"+path+"\n")
	guestProc(t, s, 100, 150, 101, "0::"+path+"\n")
	guestProc(t, s, 200, 1, 102, "0::/"+guestSidecarID+"\n")
	guestProc(t, s, 1, 0, 1, "0::/\n")
	snapshot, err := s.scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	record := snapshot.records[guestCheckoutID]
	if len(snapshot.records) != 1 || !snapshot.isComplete || record == nil {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if record.container.InitPid != 150 || record.container.CgroupPath != path || record.ref.MemoryCgroupPath != path || record.startTime != 100 || record.directory == nil {
		t.Errorf("binding=%+v", record)
	}
	if record.container.Hostname != "checkout-api" || record.container.LabelHostNamespace() != "payments" || record.container.Qos.String() != "burstable" {
		t.Errorf("metadata=%+v", record.container)
	}
}

func TestGuestScannerCgroupNamespaces(t *testing.T) {
	for _, tc := range []struct{ name, reported, actual string }{
		{"relative", "/../" + guestCheckoutID, "/workloads/" + guestCheckoutID},
		{"absolute", "/valid/" + guestCheckoutID, "/valid/" + guestCheckoutID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := guestFixture(t)
			guestDirectory(t, s.options.CgroupRoot, tc.actual)
			if tc.name == "absolute" {
				guestDirectory(t, s.options.CgroupRoot, "/other/"+guestCheckoutID)
			}
			guestProc(t, s, 150, 1, 100, "0::"+tc.reported+"\n")
			snap, err := s.scan(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := snap.records[guestCheckoutID].container.CgroupPath; got != tc.actual {
				t.Errorf("cgroup=%q, want %q", got, tc.actual)
			}
		})
	}
}

func TestGuestScannerLegacyCgroups(t *testing.T) {
	s := guestFixture(t)
	cpu := "/cpu-group/" + guestCheckoutID
	memory := "/memory-group/" + guestCheckoutID
	guestDirectory(t, s.options.CgroupRoot, "cpu,cpuacct"+cpu)
	guestDirectory(t, s.options.CgroupRoot, "memory"+memory)
	guestProc(t, s, 150, 1, 100, "2:cpu,cpuacct:"+cpu+"\n3:memory:"+memory+"\n")
	snap, err := s.scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	record := snap.records[guestCheckoutID]
	if record.container.CgroupPath != cpu || record.ref.MemoryCgroupPath != memory {
		t.Errorf("binding=%+v", record)
	}
}

func TestGuestScannerMountIdentity(t *testing.T) {
	s := guestFixture(t)
	guestFile(t, s.options.ProcRoot, "self/cgroup", "0::/\n")
	guestFile(t, s.options.ProcRoot, "self/mountinfo", "30 1 0:20 /containers/"+guestSidecarID+"/rootfs / rw - virtiofs rootfs rw\n")
	guestDirectory(t, s.options.CgroupRoot, "/"+guestCheckoutID)
	guestProc(t, s, 150, 1, 100, "0::/"+guestCheckoutID+"\n")
	snap, err := s.scan(t.Context())
	if err != nil || len(snap.records) != 1 {
		t.Errorf("mount fallback=%+v, %v", snap, err)
	}
	guestFile(t, s.options.ProcRoot, "self/mountinfo", "")
	guestFile(t, s.options.ProcRoot, "self/cgroup", "0::/unidentified\n")
	if _, err := s.scan(t.Context()); err == nil {
		t.Error("unknown sidecar identity accepted")
	}
}

func TestGuestScannerFailureAndReplacement(t *testing.T) {
	s := guestFixture(t)
	path := "/" + guestCheckoutID
	guestDirectory(t, s.options.CgroupRoot, path)
	guestProc(t, s, 150, 1, 100, "0::"+path+"\n")
	store := newTestContainerStore()
	snap, err := s.scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	previous := store.records[guestCheckoutID]
	snap, err = s.scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	if store.records[guestCheckoutID].ref.Key != previous.ref.Key || !store.records[guestCheckoutID].container.StartedAt.Equal(previous.container.StartedAt) {
		t.Error("healthy refresh replaced live instance")
	}
	// An unreadable process must not be published as a deletion.
	guestFile(t, s.options.ProcRoot, "150/stat", "malformed")
	snap, err = s.scan(t.Context())
	if err == nil || snap.isComplete {
		t.Fatal("partial scan reported complete")
	}
	store.commit(snap, err)
	if store.records[guestCheckoutID] == nil {
		t.Error("partial scan deleted live container")
	}
	guestProc(t, s, 150, 1, 200, "0::"+path+"\n")
	snap, err = s.scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	if store.records[guestCheckoutID].ref.Key == previous.ref.Key {
		t.Error("reused PID retained old generation")
	}
	if err := os.RemoveAll(filepath.Join(s.options.ProcRoot, "150")); err != nil {
		t.Fatal(err)
	}
	snap, err = s.scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	if len(store.records) != 0 {
		t.Error("complete scan retained exited container")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.scan(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled scan=%v", err)
	}
}

func TestGuestScannerAmbiguousPath(t *testing.T) {
	s := guestFixture(t)
	for _, prefix := range []string{"/first/", "/second/"} {
		guestDirectory(t, s.options.CgroupRoot, prefix+guestCheckoutID)
	}
	guestProc(t, s, 150, 1, 100, "0::/../"+guestCheckoutID+"\n")
	snap, err := s.scan(t.Context())
	if err == nil || snap.isComplete || snap.records[guestCheckoutID] != nil {
		t.Errorf("ambiguous scan=%+v, %v", snap, err)
	}
}

func TestGuestContainerReference(t *testing.T) {
	s := guestFixture(t)
	guestDirectory(t, s.options.CgroupRoot, "/"+guestCheckoutID)
	guestProc(t, s, 150, 1, 100, "0::/"+guestCheckoutID+"\n")
	snap, err := s.scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	previous := containerView
	containerView = newTestContainerStore()
	t.Cleanup(func() { containerView = previous })
	containerView.commit(snap, nil)
	ref := containerView.records[guestCheckoutID].ref
	if err := ValidateContainerRef(ref); err != nil {
		t.Errorf("Guest validation contacted host runtime: %v", err)
	}
	guestProc(t, s, 150, 1, 101, "0::/"+guestCheckoutID+"\n")
	if err := ValidateContainerRef(ref); err == nil {
		t.Error("reused process accepted")
	}
}

func TestGuestControllerLifecycle(t *testing.T) {
	s := guestFixture(t)
	store := newContainerStore()
	controller := newContainerController(store)
	controller.scan = s.scan
	controller.resyncInterval = 10 * time.Millisecond
	controller.start()
	sub := subscribeForTest(t, store)
	waitContainerViewForTest(t, sub, 0)
	controller.cancel()
	<-controller.done
	if _, err := sub.DrainEvents(); !errors.Is(err, ErrContainerSubscriptionClosed) {
		t.Errorf("shutdown subscription=%v", err)
	}
}

func BenchmarkGuestScan(b *testing.B) {
	s := guestFixture(b)
	guestDirectory(b, s.options.CgroupRoot, "/"+guestCheckoutID)
	for pid := 100; pid < 132; pid++ {
		guestProc(b, s, pid, 1, uint64(pid), "0::/"+guestCheckoutID+"\n")
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.scan(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

func TestGuestScannerPrivateCgroupRoot(t *testing.T) {
	s := guestFixture(t)
	guestFile(t, s.options.ProcRoot, "self/cgroup", "0::/\n")
	guestDirectory(t, s.options.CgroupRoot, "/workloads/"+guestCheckoutID)
	guestProc(t, s, 150, 1, 100, "0::/../"+guestCheckoutID+"\n")
	guestProc(t, s, 200, 1, 100, "0::/\n")
	snapshot, err := s.scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.records) != 1 || snapshot.records[guestCheckoutID] == nil {
		t.Errorf("private namespace snapshot=%+v", snapshot)
	}
}
