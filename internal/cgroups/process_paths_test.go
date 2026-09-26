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

package cgroups

import (
	"errors"
	"strings"
	"testing"
	"testing/iotest"
)

func TestParseProcessPaths(t *testing.T) {
	tests := []struct {
		name           string
		mode           Mode
		content        string
		wantPath       string
		hasError       bool
		hasControllers bool
	}{
		{
			name:     "cgroup v2",
			mode:     Unified,
			content:  "0::/kubepods.slice/pod.slice/cri-containerd-id.scope\n",
			wantPath: "/kubepods.slice/pod.slice/cri-containerd-id.scope",
		},
		{
			name: "cgroup v1",
			mode: Legacy,
			content: "5:memory:/kubepods/container-id\n" +
				"4:cpu,cpuacct:/kubepods/container-id\n",
			wantPath:       "/kubepods/container-id",
			hasControllers: true,
		},
		{
			// A hybrid host reports the v1 controller lines and, after them,
			// the controller-less cgroup v2 line. The v1 manager that
			// NewManager() returns for this mode can only read the former.
			name: "hybrid host",
			mode: Hybrid,
			content: "12:pids:/kubepods/container-id\n" +
				"5:memory:/kubepods/container-id\n" +
				"4:cpu,cpuacct:/kubepods/container-id\n" +
				"1:name=systemd:/kubepods/container-id\n" +
				"0::/system.slice/containerd.service\n",
			wantPath:       "/kubepods/container-id",
			hasControllers: true,
		},
		{
			name:     "invalid entry",
			mode:     Legacy,
			content:  "invalid\n",
			hasError: true,
		},
		{
			name:     "empty membership",
			mode:     Legacy,
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths, err := parseProcessPaths(strings.NewReader(tt.content))
			if tt.hasError {
				if err == nil {
					t.Fatal("parseProcessPaths() error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProcessPaths() error = %v", err)
			}
			if got := paths.Controllers != nil; got != tt.hasControllers {
				t.Fatalf("parseProcessPaths() controllers initialized = %t, want %t", got, tt.hasControllers)
			}
			got, err := paths.pathForProcesses(tt.mode)
			if err != nil {
				t.Fatalf("PathForProcesses() error = %v", err)
			}
			if got != tt.wantPath {
				t.Fatalf("PathForProcesses() = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

func TestParseProcessPathsReturnsScannerError(t *testing.T) {
	_, err := parseProcessPaths(iotest.ErrReader(errors.New("read failure")))
	if err == nil {
		t.Fatal("parseProcessPaths() error = nil, want non-nil")
	}
}

// TestPathForProcessesHostModes pins which membership each host mode resolves
// to. NewManager() reads the path through the v1 manager on legacy and hybrid
// hosts and through the v2 manager on unified hosts, and the two managers read
// it from different hierarchies, /sys/fs/cgroup/<controller>/<path>/cgroup.procs
// against /sys/fs/cgroup/<path>/cgroup.procs.
func TestPathForProcessesHostModes(t *testing.T) {
	for _, tt := range []struct {
		name  string
		mode  Mode
		paths *ProcessPaths
		want  string
	}{
		{
			// The case that used to return the unified path: a hybrid host
			// reports both memberships, and the v1 manager it gets cannot read
			// the unified one.
			name: "hybrid prefers the controller membership",
			mode: Hybrid,
			paths: &ProcessPaths{
				Unified:     "/system.slice/containerd.service",
				Controllers: map[string]string{"cpu": "/kubepods/burstable/pod/ctr-id"},
			},
			want: "/kubepods/burstable/pod/ctr-id",
		},
		{
			name:  "legacy uses the controller membership",
			mode:  Legacy,
			paths: &ProcessPaths{Controllers: map[string]string{"cpu": "/kubepods/container-id"}},
			want:  "/kubepods/container-id",
		},
		{
			name:  "unified uses the unified membership",
			mode:  Unified,
			paths: &ProcessPaths{Unified: "/kubepods.slice/pod.slice/cri-containerd-id.scope"},
			want:  "/kubepods.slice/pod.slice/cri-containerd-id.scope",
		},
		{
			// Containerd classifies a host by statfs of /sys/fs/cgroup only, so
			// the mode and the lines a process reports can disagree; keep a
			// fallback in both directions.
			name: "unified falls back to a controller membership",
			mode: Unified,
			paths: &ProcessPaths{
				Controllers: map[string]string{"cpu": "/v1-only-membership"},
			},
			want: "/v1-only-membership",
		},
		{
			name:  "hybrid falls back to the unified membership",
			mode:  Hybrid,
			paths: &ProcessPaths{Unified: "/v2-only-membership"},
			want:  "/v2-only-membership",
		},
		{
			// cpuacct and pids are fallbacks only: the caller reads the cpu
			// hierarchy, so they are interchangeable with cpu whenever the
			// runtime keeps one path across the hierarchies.
			name: "cpuacct fallback",
			mode: Hybrid,
			paths: &ProcessPaths{
				Unified:     "/unified",
				Controllers: map[string]string{"cpuacct": "/cpuacct"},
			},
			want: "/cpuacct",
		},
		{
			name: "pids fallback",
			mode: Hybrid,
			paths: &ProcessPaths{
				Unified:     "/unified",
				Controllers: map[string]string{"pids": "/pids"},
			},
			want: "/pids",
		},
		{
			name: "empty controller value is skipped",
			mode: Hybrid,
			paths: &ProcessPaths{
				Unified:     "/unified",
				Controllers: map[string]string{"cpu": "", "pids": "/pids"},
			},
			want: "/pids",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.paths.pathForProcesses(tt.mode)
			if err != nil {
				t.Fatalf("PathForProcesses() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("PathForProcesses() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProcessPathsPathForProcessesRejectsMissingHierarchy(t *testing.T) {
	tests := []struct {
		name  string
		paths *ProcessPaths
	}{
		{name: "nil paths"},
		{name: "empty paths", paths: &ProcessPaths{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.paths.PathForProcesses(); err == nil {
				t.Fatal("PathForProcesses() error = nil, want non-nil")
			}
		})
	}
}

func BenchmarkParseProcessPaths(b *testing.B) {
	const content = "5:memory:/kubepods/container-id\n" +
		"4:cpu,cpuacct:/kubepods/container-id\n"

	for b.Loop() {
		if _, err := parseProcessPaths(strings.NewReader(content)); err != nil {
			b.Fatal(err)
		}
	}
}

func TestProcessPathsPathForMemory(t *testing.T) {
	for _, tt := range []struct {
		name  string
		paths *ProcessPaths
		want  string
	}{
		{name: "nil"},
		{name: "missing", paths: &ProcessPaths{Controllers: map[string]string{"cpu": "/cpu"}}},
		{name: "v1", paths: &ProcessPaths{Controllers: map[string]string{"memory": "/memory"}}, want: "/memory"},
		{name: "v2", paths: &ProcessPaths{Unified: "/unified"}, want: "/unified"},
		{name: "hybrid", paths: &ProcessPaths{Unified: "/unified", Controllers: map[string]string{"memory": "/memory"}}, want: "/memory"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.paths.PathForMemory()
			if got != tt.want || (err != nil) != (tt.want == "") {
				t.Fatalf("PathForMemory = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
