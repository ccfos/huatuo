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
	"syscall"
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
	s := &guestScanner{
		options: GuestScannerOptions{
			ProcRoot:     t.TempDir(),
			CgroupRoot:   t.TempDir(),
			PodName:      "checkout-api",
			PodNamespace: "payments",
		},
	}
	guestFile(t, s.options.ProcRoot, "self/cgroup", "0::/"+guestSidecarID+"\n")
	guestFile(t, s.options.ProcRoot, "self/mountinfo", fmt.Sprintf(
		"40 1 0:40 / %s rw - cgroup2 none rw\n"+
			"41 1 0:41 / %s/cpu,cpuacct rw - cgroup none rw,cpu,cpuacct\n"+
			"42 1 0:42 / %s/memory rw - cgroup none rw,memory\n"+
			"43 1 0:43 / %s/blkio rw - cgroup none rw,blkio\n",
		s.options.CgroupRoot, s.options.CgroupRoot, s.options.CgroupRoot, s.options.CgroupRoot))
	return s
}

func guestDirectory(t testing.TB, root, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, path), 0o700); err != nil {
		t.Fatal(err)
	}
}

// Tests exercise the same Source and binding boundary as production startup.
func discoverGuestForTest(t testing.TB, scanner *guestScanner, ctx context.Context) (containerSnapshot, error) {
	t.Helper()
	manager, err := newSourceManager(newTestContainerStore(), scanner, ManagerOptions{ProcRoot: scanner.options.ProcRoot})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err, _ := manager.discover(ctx, nil, true)
	return snapshot, err
}

func TestGuestSourceStartupWithoutCSS(t *testing.T) {
	scanner := guestFixture(t)
	guestDirectory(t, scanner.options.CgroupRoot, "/"+guestCheckoutID)
	guestProc(t, scanner, 150, 1, 100, "0::/"+guestCheckoutID+"\n")
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
	source, err := NewGuestSource(scanner.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := StartManager(source, ManagerOptions{ProcRoot: scanner.options.ProcRoot, SyncInterval: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	sub := subscribeForTest(t, containerView)
	waitContainerViewForTest(t, sub, 1)
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
	snapshot, err := discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	record := snapshot.records[guestCheckoutID]
	if len(snapshot.records) != 1 || !snapshot.isComplete || record == nil {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if record.container.InitPid != 150 ||
		record.container.CgroupPath != path ||
		record.ref.MemoryCgroupPath != path ||
		record.startTime != 100 ||
		record.directory == nil {
		t.Errorf("binding=%+v", record)
	}
	if record.container.Hostname != "checkout-api" ||
		record.container.LabelHostNamespace() != "payments" ||
		record.container.Qos.String() != "burstable" {
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
			snap, err := discoverGuestForTest(t, s, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got := snap.records[guestCheckoutID].container.CgroupPath; got != tc.actual {
				t.Errorf("cgroup=%q, want %q", got, tc.actual)
			}
		})
	}
}

// TestGuestScannerLegacyCgroups 验证 CPU 与 memory 分属不同 v1 路径时，
// 容器和生命周期引用保留 memory 路径；缺少 memory 控制器时不回退到 CPU 路径。
func TestGuestScannerLegacyCgroups(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hasMemory bool
	}{
		{name: "separate-hierarchies", hasMemory: true},
		{name: "no-memory-controller"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanner := guestFixture(t)
			cpu := "/cpu-group/" + guestCheckoutID
			var memory string
			membership := "2:cpu,cpuacct:" + cpu + "\n"
			guestDirectory(t, scanner.options.CgroupRoot, "cpu,cpuacct"+cpu)
			if tc.hasMemory {
				memory = "/memory-group/" + guestCheckoutID
				guestDirectory(t, scanner.options.CgroupRoot, "memory"+memory)
				membership += "3:memory:" + memory + "\n"
			}
			guestProc(t, scanner, 150, 1, 100, membership)
			snapshot, err := discoverGuestForTest(t, scanner, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			record := snapshot.records[guestCheckoutID]
			if record == nil {
				t.Fatal("Guest container not discovered")
			}
			if record.container.CgroupPath != cpu || record.container.MemoryCgroupPath() != memory ||
				record.ref.MemoryCgroupPath != memory {
				t.Fatalf("incorrect memory binding: %+v", record)
			}
		})
	}
}

func TestGuestScannerMountIdentity(t *testing.T) {
	s := guestFixture(t)
	guestFile(t, s.options.ProcRoot, "self/cgroup", "0::/\n")
	guestFile(
		t,
		s.options.ProcRoot,
		"self/mountinfo",
		"30 1 0:20 /containers/"+guestSidecarID+"/rootfs / rw - virtiofs rootfs rw\n",
	)
	guestDirectory(t, s.options.CgroupRoot, "/"+guestCheckoutID)
	guestProc(t, s, 150, 1, 100, "0::/"+guestCheckoutID+"\n")
	snap, err := discoverGuestForTest(t, s, t.Context())
	if err != nil || len(snap.records) != 1 {
		t.Errorf("mount fallback=%+v, %v", snap, err)
	}
	guestFile(t, s.options.ProcRoot, "self/mountinfo", "")
	guestFile(t, s.options.ProcRoot, "self/cgroup", "0::/unidentified\n")
	if _, err := discoverGuestForTest(t, s, t.Context()); err == nil {
		t.Error("unknown sidecar identity accepted")
	}
}

func TestGuestScannerFailureAndReplacement(t *testing.T) {
	s := guestFixture(t)
	path := "/" + guestCheckoutID
	guestDirectory(t, s.options.CgroupRoot, path)
	guestProc(t, s, 150, 1, 100, "0::"+path+"\n")
	store := newTestContainerStore()
	snap, err := discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	previous := store.records[guestCheckoutID]
	snap, err = discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	if store.records[guestCheckoutID].ref.Key != previous.ref.Key ||
		!store.records[guestCheckoutID].container.StartedAt.Equal(
			previous.container.StartedAt,
		) {
		t.Error("healthy refresh replaced live instance")
	}
	// An unreadable process must not be published as a deletion.
	guestFile(t, s.options.ProcRoot, "150/stat", "malformed")
	snap, err = discoverGuestForTest(t, s, t.Context())
	if err == nil || snap.isComplete {
		t.Fatal("partial scan reported complete")
	}
	store.commit(snap, err)
	if store.records[guestCheckoutID] == nil {
		t.Error("partial scan deleted live container")
	}
	guestProc(t, s, 150, 1, 200, "0::"+path+"\n")
	snap, err = discoverGuestForTest(t, s, t.Context())
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
	snap, err = discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	if len(store.records) != 0 {
		t.Error("complete scan retained exited container")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := discoverGuestForTest(t, s, ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled scan=%v", err)
	}
}

func TestGuestScannerAmbiguousPath(t *testing.T) {
	s := guestFixture(t)
	path := "/workloads/" + guestCheckoutID
	guestDirectory(t, s.options.CgroupRoot, path)
	guestProc(t, s, 150, 1, 100, "0::"+path+"\n")

	store := newTestContainerStore()
	snap, err := discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	previous := store.records[guestCheckoutID]
	if previous == nil {
		t.Fatal("healthy container was not published")
	}

	// A namespace-relative path matching two directories cannot be bound, but
	// the container ID is still known.
	guestDirectory(t, s.options.CgroupRoot, "/other/"+guestCheckoutID)
	guestFile(t, s.options.ProcRoot, "150/cgroup", "0::/../"+guestCheckoutID+"\n")
	snap, err = discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatalf("binding metadata must not fail the scan: %v", err)
	}
	if !snap.isComplete {
		t.Fatal("known container ID must keep the membership view complete")
	}
	if snap.records[guestCheckoutID] != nil {
		t.Fatal("ambiguous binding must stay pending")
	}
	store.commit(snap, nil)
	if store.records[guestCheckoutID] != previous {
		t.Fatal("pending metadata replaced the live instance")
	}
	if store.err != nil {
		t.Fatalf("view became unavailable: %v", store.err)
	}

	// Removing the ambiguity lets the next scan bind again.
	if err := os.RemoveAll(filepath.Join(s.options.CgroupRoot, "other")); err != nil {
		t.Fatal(err)
	}
	snap, err = discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	if store.records[guestCheckoutID] == nil {
		t.Fatal("metadata did not recover")
	}
}

func TestGuestScannerBindingFailureKeepsMembership(t *testing.T) {
	s := guestFixture(t)
	healthyPath := "/workloads/" + guestCheckoutID
	guestDirectory(t, s.options.CgroupRoot, healthyPath)
	guestProc(t, s, 150, 1, 100, "0::"+healthyPath+"\n")

	missingID := strings.Repeat("c", 64)
	guestProc(t, s, 200, 1, 200, "0::/missing/"+missingID+"\n")

	store := newTestContainerStore()
	snap, err := discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatalf("unresolvable container must not fail the scan: %v", err)
	}
	if !snap.isComplete {
		t.Fatal("known container IDs must keep the membership view complete")
	}
	if snap.records[missingID] != nil {
		t.Fatal("missing cgroup produced a record")
	}
	if snap.records[guestCheckoutID] == nil {
		t.Fatal("healthy container was not bound")
	}
	store.commit(snap, nil)
	if store.err != nil {
		t.Fatalf("view became unavailable: %v", store.err)
	}
	if store.records[guestCheckoutID] == nil {
		t.Fatal("healthy container was not published")
	}

	// The healthy container exits while another container stays unresolvable;
	// deletion must still proceed for the known-exited ID.
	if err := os.RemoveAll(filepath.Join(s.options.ProcRoot, "150")); err != nil {
		t.Fatal(err)
	}
	snap, err = discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snap, nil)
	if _, ok := store.records[guestCheckoutID]; ok {
		t.Fatal("exited container was not deleted")
	}
	if store.err != nil {
		t.Fatalf("view became unavailable: %v", store.err)
	}
}

func TestGuestContainerReference(t *testing.T) {
	for _, mode := range []string{"unified", "private unified", "private v1", "v1 without memory"} {
		t.Run(mode, func(t *testing.T) {
			s := guestFixture(t)
			path := "/workloads/" + guestCheckoutID
			membership := "0::" + path + "\n"
			switch mode {
			case "unified":
				guestDirectory(t, s.options.CgroupRoot, path)
			case "private unified":
				guestDirectory(t, s.options.CgroupRoot, path)
				membership = "0::/../" + guestCheckoutID + "\n"
			case "private v1":
				guestDirectory(t, s.options.CgroupRoot, "cpu,cpuacct"+path)
				guestDirectory(t, s.options.CgroupRoot, "memory"+path)
				membership = "2:cpu,cpuacct:/../" + guestCheckoutID + "\n3:memory:/../" + guestCheckoutID + "\n"
			case "v1 without memory":
				guestDirectory(t, s.options.CgroupRoot, "cpu,cpuacct"+path)
				membership = "2:cpu,cpuacct:" + path + "\n"
			}
			guestProc(t, s, 150, 1, 100, membership)
			snap, err := discoverGuestForTest(t, s, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			previous := containerView
			containerView = newTestContainerStore()
			t.Cleanup(func() { containerView = previous })
			containerView.commit(snap, nil)
			ref := containerView.records[guestCheckoutID].ref
			if err := ValidateContainerResources(ref); err != nil {
				t.Errorf("Guest validation contacted host runtime or lost binding: %v", err)
			}
			guestProc(t, s, 150, 1, 101, membership)
			if err := ValidateContainerResources(ref); err == nil {
				t.Error("reused process accepted")
			}
		})
	}
}

func TestGuestControllerLifecycle(t *testing.T) {
	s := guestFixture(t)
	store := newContainerStore()
	controller, err := newSourceManager(store, s, ManagerOptions{ProcRoot: s.options.ProcRoot})
	if err != nil {
		t.Fatal(err)
	}
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
	manager, err := newSourceManager(newTestContainerStore(), s, ManagerOptions{ProcRoot: s.options.ProcRoot})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err, _ := manager.discover(b.Context(), nil, true); err != nil {
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
	snapshot, err := discoverGuestForTest(t, s, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.records) != 1 || snapshot.records[guestCheckoutID] == nil {
		t.Errorf("private namespace snapshot=%+v", snapshot)
	}
}

// TestGuestScannerPublicResources 验证 Guest 的 v1 分控制器路径和 v2 统一路径
// 都经公共绑定发布，保留独立 IO 路径、代表进程和 legacy 视图；没有 netns
// 定位时不补出网络目标，旧的私有 cgroup 命名空间路径仍可解析。
func TestGuestScannerPublicResources(t *testing.T) {
	cases := []struct {
		name        string
		membership  string
		directories []string
		want        map[string]string
	}{
		{
			name:        "legacy split controllers",
			membership:  "2:cpu,cpuacct:/cpu-group/" + guestCheckoutID + "\n3:memory:/memory-group/" + guestCheckoutID + "\n4:blkio:/io-group/" + guestCheckoutID + "\n",
			directories: []string{"cpu,cpuacct/cpu-group/" + guestCheckoutID, "memory/memory-group/" + guestCheckoutID, "blkio/io-group/" + guestCheckoutID},
			want:        map[string]string{"cpu": "/cpu-group/" + guestCheckoutID, "cpuacct": "/cpu-group/" + guestCheckoutID, "memory": "/memory-group/" + guestCheckoutID, "blkio": "/io-group/" + guestCheckoutID},
		},
		{
			name:        "legacy private namespaces",
			membership:  "2:cpu,cpuacct:/../" + guestCheckoutID + "\n3:memory:/../" + guestCheckoutID + "\n4:blkio:/../" + guestCheckoutID + "\n",
			directories: []string{"cpu,cpuacct/workloads/" + guestCheckoutID, "memory/workloads/" + guestCheckoutID, "blkio/workloads/" + guestCheckoutID},
			want:        map[string]string{"cpu": "/workloads/" + guestCheckoutID, "cpuacct": "/workloads/" + guestCheckoutID, "memory": "/workloads/" + guestCheckoutID, "blkio": "/workloads/" + guestCheckoutID},
		},
		{
			name:        "unified private namespace",
			membership:  "0::/../" + guestCheckoutID + "\n",
			directories: []string{"workloads/" + guestCheckoutID},
			want:        map[string]string{"cpu": "/workloads/" + guestCheckoutID, "memory": "/workloads/" + guestCheckoutID},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scanner := guestFixture(t)
			for _, directory := range tc.directories {
				guestDirectory(t, scanner.options.CgroupRoot, directory)
			}
			guestProc(t, scanner, 150, 1, 100, tc.membership)
			memoryDirectory := filepath.Join(scanner.options.CgroupRoot, tc.want["memory"])
			if strings.Contains(tc.membership, ":memory:") {
				memoryDirectory = filepath.Join(scanner.options.CgroupRoot, "memory", tc.want["memory"])
			}
			var stat syscall.Stat_t
			if err := syscall.Stat(memoryDirectory, &stat); err != nil {
				t.Error(err)
				return
			}
			// Resource ownership comes from the directory, not a parsed container ID.
			const memoryCSS = 8500
			cgroupCssMetaDataMap.Store(uint64(memoryCSS), &containerCssMetaData{CSS: memoryCSS, Inode: stat.Ino, SubSys: "memory"})
			t.Cleanup(func() { cgroupCssMetaDataMap.Delete(uint64(memoryCSS)) })
			snapshot, err := discoverGuestForTest(t, scanner, t.Context())
			if err != nil {
				t.Errorf("scan Guest: %v", err)
				return
			}
			store := newTestContainerStore()
			store.commit(snapshot, nil)
			record := store.records[guestCheckoutID]
			if record == nil || record.pod == nil {
				t.Error("Guest workload was not published")
				return
			}
			for controller, want := range tc.want {
				if got, available := record.pod.Cgroups.Path(controller); !available || got != want {
					t.Errorf("%s path=%q available=%t, want %q", controller, got, available, want)
				}
			}
			if record.container.CgroupCss["memory"] != memoryCSS || record.pod.Cgroups.CSS["memory"] != memoryCSS {
				t.Error("Guest legacy and public views did not share the verified memory CSS")
			}
			if record.container.CgroupPath != tc.want["cpu"] || record.container.MemoryCgroupPath() != tc.want["memory"] {
				t.Error("legacy Guest paths changed")
			}
			if pid, err := record.pod.ProcessPID(); err != nil || pid != 150 {
				t.Errorf("Guest representative PID=%d, err=%v", pid, err)
			}
			if pid, err := record.pod.NetworkPID(); err == nil || pid != 0 {
				t.Errorf("Guest fabricated network PID=%d, err=%v", pid, err)
			}
		})
	}
}

func TestGuestSourceReturnsLocations(t *testing.T) {
	scanner := guestFixture(t)
	guestDirectory(t, scanner.options.CgroupRoot, "/"+guestCheckoutID)
	guestProc(t, scanner, 150, 1, 100, "0::/"+guestCheckoutID+"\n")
	discovery, err := scanner.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	workload := discovery.Pods[guestCheckoutID]
	if !discovery.Complete || workload == nil || workload.Type != TypeContainer || scanner.Type() != TypeContainer {
		t.Errorf("Guest discovery=%+v", discovery)
		return
	}
	if !workload.SyncedAt.IsZero() || workload.lifeResources != nil || workload.Cgroups.CSS != nil ||
		workload.Cgroups.directories != nil || workload.Process.startTime != 0 || workload.Network != nil {
		t.Error("Guest source published bound resources or collection state")
	}
	store := newTestContainerStore()
	manager, err := newSourceManager(store, scanner, ManagerOptions{ProcRoot: scanner.options.ProcRoot})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.bindDiscovery(t.Context(), TypeContainer, discovery)
	if err != nil {
		t.Fatal(err)
	}
	store.commit(snapshot, nil)
	record := store.records[guestCheckoutID]
	if record == nil || record.pod.Process == nil || record.pod.Process.startTime != 100 || record.directory == nil {
		t.Error("Manager did not bind Guest locations")
	}
	if workload.Process.startTime != 0 || workload.Cgroups.directories != nil {
		t.Error("Manager mutated the source result")
	}
}

func TestGuestReferenceMembershipAndDirectory(t *testing.T) {
	for _, change := range []string{"container ID", "memory membership", "memory directory", "ambiguous private path"} {
		t.Run(change, func(t *testing.T) {
			scanner := guestFixture(t)
			cpu, memory := "/cpu/"+guestCheckoutID, "/memory/"+guestCheckoutID
			guestDirectory(t, scanner.options.CgroupRoot, "cpu,cpuacct"+cpu)
			guestDirectory(t, scanner.options.CgroupRoot, "memory"+memory)
			membership := "2:cpu,cpuacct:" + cpu + "\n3:memory:" + memory + "\n"
			guestProc(t, scanner, 150, 1, 100, membership)
			snapshot, err := discoverGuestForTest(t, scanner, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			previous := containerView
			containerView = newTestContainerStore()
			t.Cleanup(func() { containerView = previous })
			containerView.commit(snapshot, nil)
			ref := containerView.records[guestCheckoutID].ref
			if err := ValidateContainerResources(ref); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "container ID":
				guestProc(t, scanner, 150, 1, 100, strings.ReplaceAll(membership, guestCheckoutID, strings.Repeat("c", 64)))
			case "memory membership":
				guestDirectory(t, scanner.options.CgroupRoot, "memory/other/"+guestCheckoutID)
				guestProc(t, scanner, 150, 1, 100, strings.ReplaceAll(membership, memory, "/other/"+guestCheckoutID))
			case "memory directory":
				directory := filepath.Join(scanner.options.CgroupRoot, "memory", memory)
				if err := os.Rename(directory, directory+"-old"); err != nil {
					t.Fatal(err)
				}
				guestDirectory(t, scanner.options.CgroupRoot, "memory"+memory)
			case "ambiguous private path":
				guestDirectory(t, scanner.options.CgroupRoot, "memory/other/"+guestCheckoutID)
				guestProc(t, scanner, 150, 1, 100, strings.ReplaceAll(membership, memory, "/../"+guestCheckoutID))
			}
			if err := ValidateContainerResources(ref); err == nil {
				t.Error("changed Guest binding was accepted")
			}
		})
	}
}

func TestNewGuestSource(t *testing.T) {
	for _, test := range []struct {
		name, podName, namespace string
		wantErr                  bool
	}{
		{name: "valid", podName: "checkout", namespace: "payments"},
		{name: "missing name", namespace: "payments", wantErr: true},
		{name: "missing namespace", podName: "checkout", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := activeManager
			source, err := NewGuestSource(GuestScannerOptions{PodName: test.podName, PodNamespace: test.namespace})
			if (err != nil) != test.wantErr {
				t.Errorf("constructor error=%v, want error=%t", err, test.wantErr)
			}
			if activeManager != previous {
				t.Error("constructing a source started the Manager")
			}
			if source != nil {
				scanner := source.(*guestScanner)
				if scanner.options.ProcRoot == "" || scanner.options.CgroupRoot == "" {
					t.Error("default scan roots are missing")
				}
			}
		})
	}
}
