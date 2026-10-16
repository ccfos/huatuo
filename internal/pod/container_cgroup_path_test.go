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

import "testing"

func TestEscapeSystemd(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{in: "pod1234-abcd-5678", want: "pod1234_abcd_5678"},
		{in: "already_escaped", want: "already_escaped"},
		{in: "plain", want: "plain"},
	}

	for _, tt := range tests {
		if got := escapeSystemd(tt.in); got != tt.want {
			t.Fatalf("escapeSystemd(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// expandSystemdSlice mirrors systemd's slice hierarchy convention: a dash
// introduces a parent slice, so test-a-b.slice becomes
// /test.slice/test-a.slice/test-a-b.slice.
func TestExpandSystemdSlice(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{in: "test.slice", want: "/test.slice"},
		{in: "test-a.slice", want: "/test.slice/test-a.slice"},
		{in: "test-a-b.slice", want: "/test.slice/test-a.slice/test-a-b.slice"},
	}

	for _, tt := range tests {
		if got := expandSystemdSlice(tt.in); got != tt.want {
			t.Fatalf("expandSystemdSlice(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestCgroupPathConversions pins the two path dialects used to locate a
// container's cgroup: the systemd slice/scope tree and the plain cgroupfs
// directory join.
func TestCgroupPathConversions(t *testing.T) {
	tests := []struct {
		name      string
		path      cgroupPath
		wantSysd  string
		wantCgrfs string
	}{
		{
			name:      "burstable pod",
			path:      cgroupPath{slices: []string{"kubepods", "burstable", "pod1234-abcd-5678-efgh"}},
			wantSysd:  "/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod1234_abcd_5678_efgh.slice",
			wantCgrfs: "/kubepods/burstable/pod1234-abcd-5678-efgh",
		},
		{
			name:      "guaranteed pod",
			path:      cgroupPath{slices: []string{"kubepods", "pod1234-abcd-5678-efgh"}},
			wantSysd:  "/kubepods.slice/kubepods-pod1234_abcd_5678_efgh.slice",
			wantCgrfs: "/kubepods/pod1234-abcd-5678-efgh",
		},
		{
			name:      "with container scope",
			path:      cgroupPath{slices: []string{"kubepods", "besteffort", "pod1"}, scope: "cri-containerd-abc.scope"},
			wantSysd:  "/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod1.slice/cri-containerd-abc.scope",
			wantCgrfs: "/kubepods/besteffort/pod1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.path.ToSystemd(); got != tt.wantSysd {
				t.Fatalf("ToSystemd() = %q, want %q", got, tt.wantSysd)
			}

			if got := tt.path.ToCgroupfs(); got != tt.wantCgrfs {
				t.Fatalf("ToCgroupfs() = %q, want %q", got, tt.wantCgrfs)
			}
		})
	}
}
