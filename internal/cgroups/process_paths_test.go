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
		content        string
		wantPath       string
		hasError       bool
		hasControllers bool
	}{
		{
			name:     "cgroup v2",
			content:  "0::/kubepods.slice/pod.slice/cri-containerd-id.scope\n",
			wantPath: "/kubepods.slice/pod.slice/cri-containerd-id.scope",
		},
		{
			name: "cgroup v1",
			content: "5:memory:/kubepods/container-id\n" +
				"4:cpu,cpuacct:/kubepods/container-id\n",
			wantPath:       "/kubepods/container-id",
			hasControllers: true,
		},
		{
			name:     "invalid entry",
			content:  "invalid\n",
			hasError: true,
		},
		{
			name:     "empty membership",
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
			got, err := paths.PathForProcesses()
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
