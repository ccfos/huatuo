// Copyright 2025, 2026 The HuaTuo Authors
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
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups"

	"github.com/cilium/ebpf/btf"
)

func TestCgroupSubSysIDNameMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		values    []btf.EnumValue
		want      map[int]string
		wantError string
	}{
		{
			name: "maps valid values and ignores unrelated or out-of-range values",
			values: []btf.EnumValue{
				{Name: "cpuset_cgrp_id", Value: 0},
				{Name: "cpu_cgrp_id", Value: 1},
				{Name: "unrelated", Value: 2},
				{Name: "io_cgrp_id", Value: 3},
				{Name: "memory_cgrp_id", Value: 4},
				{Name: "CGROUP_SUBSYS_COUNT", Value: 13},
				{Name: "future_cgrp_id", Value: 13},
			},
			want: map[int]string{
				0: "cpuset",
				1: "cpu",
				3: "blkio",
				4: "memory",
			},
		},
		{
			name:   "accepts a sparse kernel configuration",
			values: []btf.EnumValue{{Name: "memory_cgrp_id", Value: 4}},
			want:   map[int]string{4: "memory"},
		},
		{
			name: "rejects no subsystem values",
			values: []btf.EnumValue{
				{Name: "CGROUP_SUBSYS_COUNT", Value: 13},
				{Name: "future_cgrp_id", Value: 13},
			},
			wantError: "cgroup_subsys_id has no subsystem values",
		},
		{
			name: "rejects duplicate IDs",
			values: []btf.EnumValue{
				{Name: "cpu_cgrp_id", Value: 1},
				{Name: "memory_cgrp_id", Value: 1},
			},
			wantError: `cgroup subsystem id 1 maps to both "cpu" and "memory"`,
		},
		{
			name: "rejects duplicate normalized names",
			values: []btf.EnumValue{
				{Name: "io_cgrp_id", Value: 3},
				{Name: "blkio_cgrp_id", Value: 4},
			},
			wantError: `cgroup subsystem "blkio" maps to both ids 3 and 4`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := cgroupSubSysIDNameMap(tt.values)
			if tt.wantError != "" {
				if err == nil {
					t.Fatalf("cgroupSubSysIDNameMap() error = nil, want %q", tt.wantError)
				}
				if !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("cgroupSubSysIDNameMap() error = %q, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("cgroupSubSysIDNameMap() error = %v", err)
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("cgroupSubSysIDNameMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractContainerID(t *testing.T) {
	for _, tc := range []struct {
		input    string
		expected string
	}{
		{ // docker container cgroup name
			input:    "c2b95e61271060bef9a8b832e50c81f5eed60b788ff8a42b173c4a694c284a77",
			expected: "c2b95e61271060bef9a8b832e50c81f5eed60b788ff8a42b173c4a694c284a77",
		},
		{ // docker pod cgroup name
			input:    "pod66384b12-8f16-45f5-b520-f378e0f491fe",
			expected: "",
		},
		{ // containerd pod cgroup name
			input:    "kubepods-burstable-pod44e9d203_d0d2_4d44_a5da_702190080eb4.slice",
			expected: "",
		},
		{ // containerd container cgroup name
			input:    "cri-containerd-bd23762346b2af6261d285e8c2bdf82f9abeb427338c086cca27da98fee4dfa5.scope",
			expected: "bd23762346b2af6261d285e8c2bdf82f9abeb427338c086cca27da98fee4dfa5",
		},
	} {
		actual := extractContainerID(tc.input)
		if actual != tc.expected {
			t.Errorf("parseContainerID input %s want %s  actual %s", tc.input, tc.expected, actual)
		}
	}
}

func TestCgroupMembershipForSubsystem(t *testing.T) {
	paths := &cgroups.ProcessPaths{
		Controllers: map[string]string{"cpu": "/cpu/container", "memory": "/memory/container"},
		Unified:     "/unified/container",
	}
	tests := []struct {
		name          string
		paths         *cgroups.ProcessPaths
		mode          cgroups.Mode
		subsystem     string
		expectedPath  string
		expectedError string
	}{
		{name: "v1 memory uses its own hierarchy", paths: paths, mode: cgroups.Legacy, subsystem: "memory", expectedPath: "/memory/container"},
		{name: "v1 CPU uses its own hierarchy", paths: paths, mode: cgroups.Legacy, subsystem: "cpu", expectedPath: "/cpu/container"},
		{name: "hybrid memory ignores unified membership", paths: paths, mode: cgroups.Hybrid, subsystem: "memory", expectedPath: "/memory/container"},
		{name: "v2 uses unified membership", paths: paths, mode: cgroups.Unified, subsystem: "memory", expectedPath: "/unified/container"},
		{
			name:  "missing v1 memory does not fall back to CPU or v2",
			paths: &cgroups.ProcessPaths{Controllers: map[string]string{"cpu": "/cpu/container"}, Unified: "/unified/container"},
			mode:  cgroups.Legacy, subsystem: "memory", expectedError: "no memory cgroup v1 membership",
		},
		{
			name:  "missing v2 membership does not fall back to v1",
			paths: &cgroups.ProcessPaths{Controllers: map[string]string{"memory": "/memory/container"}},
			mode:  cgroups.Unified, subsystem: "memory", expectedError: "no cgroup v2 membership",
		},
		{name: "missing paths", mode: cgroups.Legacy, subsystem: "memory", expectedError: "missing process cgroup membership"},
		{name: "unsupported mode", paths: paths, mode: cgroups.Mode(255), subsystem: "memory", expectedError: "unsupported cgroup mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actualPath, actualErr := cgroupMembershipForSubsystem(test.paths, test.mode, test.subsystem)
			if test.expectedError != "" {
				if actualErr == nil || !strings.Contains(actualErr.Error(), test.expectedError) {
					t.Fatalf("expected error %q, got path %q and error %v", test.expectedError, actualPath, actualErr)
				}
				return
			}
			if actualErr != nil || actualPath != test.expectedPath {
				t.Fatalf("expected path %q, got %q and error %v", test.expectedPath, actualPath, actualErr)
			}
		})
	}
}

func TestResolveCgroupFilesystemPath(t *testing.T) {
	realRoot := t.TempDir()
	symlinkParent := t.TempDir()
	symlinkRoot := filepath.Join(symlinkParent, "cpu")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	membershipPath := "/kubepods/container"
	cgroupPath := filepath.Join(realRoot, "kubepods", "container")
	if err := os.MkdirAll(cgroupPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cgroupPath, cgroupv1NotifyFile), nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := resolveCgroupFilesystemPath(symlinkRoot, membershipPath, cgroupv1NotifyFile)
	if err != nil {
		t.Fatalf("resolveCgroupFilesystemPath() error = %v", err)
	}
	if got != cgroupPath {
		t.Fatalf("resolveCgroupFilesystemPath() = %q, want %q", got, cgroupPath)
	}
}

func TestResolveCgroupFilesystemPathRejectsMissingNotificationFile(t *testing.T) {
	root := t.TempDir()
	cgroupPath := filepath.Join(root, "kubepods", "container")
	if err := os.MkdirAll(cgroupPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	_, err := resolveCgroupFilesystemPath(root, "/kubepods/container", cgroupv2NotifyFile)
	if err == nil {
		t.Fatal("resolveCgroupFilesystemPath() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), filepath.Join(cgroupPath, cgroupv2NotifyFile)) {
		t.Fatalf("resolveCgroupFilesystemPath() error = %q, want notification path", err)
	}
}

func TestCgroupSubsystemInitializationRetries(t *testing.T) {
	oldIDs, oldLoader := cgroupCssID2SubSysNameMap, cgroupSubSysLoader
	t.Cleanup(func() { cgroupCssID2SubSysNameMap, cgroupSubSysLoader = oldIDs, oldLoader })
	cgroupCssID2SubSysNameMap = nil
	calls := 0
	cgroupSubSysLoader = func() error {
		calls++
		if calls == 1 {
			return errors.New("temporary BTF failure")
		}
		cgroupCssID2SubSysNameMap = map[int]string{0: "memory"}
		return nil
	}
	if cgroupInitSubSysIDs() == nil || cgroupInitSubSysIDs() != nil || cgroupInitSubSysIDs() != nil || calls != 2 {
		t.Fatalf("initialization did not retry failure and cache success: calls=%d", calls)
	}
}

type lifecycleReaderTest struct {
	bpf.PerfEventReader
	event     *containerCssPerfEvent
	closed    chan struct{}
	once      sync.Once
	fail      bool
	processed chan struct{}
}

func (r *lifecycleReaderTest) ReadInto(dst any) error {
	if r.fail {
		return errors.New("reader failed")
	}
	if r.event != nil {
		*dst.(*containerCssPerfEvent) = *r.event
		r.event = nil
		return nil
	}
	if r.processed != nil {
		close(r.processed)
		r.processed = nil
	}
	<-r.closed
	return context.Canceled
}

func (r *lifecycleReaderTest) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

// TestCgroupReaderRecoversAndStops 验证 reader 故障后的全量恢复、基于已绑定
// CSS 的定向提示，以及取消后的正常退出；归属不从目录名称推断。
func TestCgroupReaderRecoversAndStops(t *testing.T) {
	store := newTestContainerStore()
	oldView := containerView
	containerView = store
	t.Cleanup(func() { containerView = oldView })
	id := strings.Repeat("a", 64)
	record := containerRecordForTest(id)
	record.pod = podFromContainer(record.container)
	record.pod.Cgroups = CgroupBinding{CSS: map[string]uint64{"cpu": 9150}, directories: map[string]string{"cpu": "/bound-checkout"}}
	store.commit(containerSnapshot{records: map[string]*containerRecord{id: record}, isComplete: true}, nil)
	controller := newContainerController(store)
	workloadManagerMu.Lock()
	previous := activeManager
	activeManager = controller.workloadManager
	workloadManagerMu.Unlock()
	t.Cleanup(func() { workloadManagerMu.Lock(); activeManager = previous; workloadManagerMu.Unlock() })
	event := &containerCssPerfEvent{Operation: abi.CgroupCSSOperationUpdate}
	event.CSS[0] = 9150
	copy(event.KnodeName[:], "8")
	first := &lifecycleReaderTest{closed: make(chan struct{}), fail: true}
	next := &lifecycleReaderTest{closed: make(chan struct{}), event: event}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseCgroupCssEventLoop(ctx, first, func() (bpf.PerfEventReader, error) { return next, nil })
	}()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case <-controller.wake:
		case <-timeout:
			t.Fatal("reader did not recover")
		}
		controller.mu.Lock()
		_, received := controller.hints[id]
		full := controller.full
		controller.mu.Unlock()
		if received {
			if !full {
				t.Fatal("reconnection did not request full view")
			}
			break
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reader did not stop")
	}
}

func TestCPURefreshDoesNotRetriggerDiscovery(t *testing.T) {
	oldNames := cgroupCssID2SubSysNameMap
	cgroupCssID2SubSysNameMap = map[int]string{0: "cpu"}
	t.Cleanup(func() { cgroupCssID2SubSysNameMap = oldNames; cgroupCssMetaDataMap.Delete(uint64(765432)) })
	controller := newContainerController(newTestContainerStore())
	controller.full = false
	workloadManagerMu.Lock()
	previous := activeManager
	activeManager = controller.workloadManager
	workloadManagerMu.Unlock()
	t.Cleanup(func() { workloadManagerMu.Lock(); activeManager = previous; workloadManagerMu.Unlock() })
	event := &containerCssPerfEvent{Operation: abi.CgroupCSSOperationRefresh, Inode: 42}
	event.CSS[0] = 765432
	copy(event.KnodeName[:], strings.Repeat("a", 64))
	processed := make(chan struct{})
	reader := &lifecycleReaderTest{closed: make(chan struct{}), event: event, processed: processed}
	ctx, cancel := context.WithCancel(t.Context())
	done := cgroupCssEventSyncHandler(ctx, reader, true)
	t.Cleanup(func() { cancel(); reader.Close(); <-done })
	select {
	case <-processed:
	case <-time.After(time.Second):
		t.Fatal("CSS refresh was not processed")
	}
	if _, ok := cgroupCssMetaDataMap.Load(uint64(765432)); !ok {
		t.Error("CPU refresh did not populate the cache")
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.full || len(controller.hints) != 0 {
		t.Errorf("read-time refresh produced lifecycle hints: %+v", controller.hints)
	}
}

// TestUnknownCgroupEventRequestsFullRefresh 验证无法归属的创建和删除事件只
// 请求合并全量刷新，数字目录或看似容器 ID 的名称都不会生成成员变更。
func TestUnknownCgroupEventRequestsFullRefresh(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprintf("removed=%t", removed), func(t *testing.T) {
			store := newTestContainerStore()
			previousView := containerView
			containerView = store
			controller := newContainerController(store)
			controller.full = false
			workloadManagerMu.Lock()
			previous := activeManager
			activeManager = controller.workloadManager
			workloadManagerMu.Unlock()
			t.Cleanup(func() {
				containerView = previousView
				workloadManagerMu.Lock()
				activeManager = previous
				workloadManagerMu.Unlock()
			})
			event := &containerCssPerfEvent{Inode: 4200, Operation: abi.CgroupCSSOperationUpdate}
			if removed {
				event.Operation = abi.CgroupCSSOperationRemove
			}
			copy(event.KnodeName[:], "8")
			processed := make(chan struct{})
			reader := &lifecycleReaderTest{closed: make(chan struct{}), event: event, processed: processed}
			ctx, cancel := context.WithCancel(t.Context())
			done := cgroupCssEventSyncHandler(ctx, reader, true)
			t.Cleanup(func() { cancel(); _ = reader.Close(); <-done })
			select {
			case <-processed:
			case <-time.After(time.Second):
				t.Error("unknown cgroup event was not processed")
				return
			}
			controller.mu.Lock()
			defer controller.mu.Unlock()
			if !controller.full || len(controller.hints) != 0 || len(store.records) != 0 {
				t.Error("unknown event created a targeted hint or changed membership")
			}
		})
	}
}
