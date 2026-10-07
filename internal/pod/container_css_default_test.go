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
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"

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
	event  *containerCssPerfEvent
	closed chan struct{}
	once   sync.Once
	fail   bool
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
	<-r.closed
	return context.Canceled
}

func (r *lifecycleReaderTest) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func TestCgroupReaderRecoversAndStops(t *testing.T) {
	controller := newContainerController(newTestContainerStore())
	containerManagerMu.Lock()
	previous := containerManager
	containerManager = controller
	containerManagerMu.Unlock()
	t.Cleanup(func() { containerManagerMu.Lock(); containerManager = previous; containerManagerMu.Unlock() })
	id := strings.Repeat("a", 64)
	event := &containerCssPerfEvent{Operation: abi.CgroupCSSOperationUpdate}
	copy(event.KnodeName[:], id)
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
