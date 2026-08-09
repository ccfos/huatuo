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

func TestCgroupPathRenderers(t *testing.T) {
	tests := []struct {
		name string
		path cgroupPath
		wantSystemd string
		wantCgroupfs string
	}{
		{
			name: "pod path with systemd scope",
			path: cgroupPath{slices: []string{"kubepods", "burstable", "pod1234-abcd"}, scope: "cri-containerd-a.scope"},
			wantSystemd: "/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod1234_abcd.slice/cri-containerd-a.scope",
			wantCgroupfs: "/kubepods/burstable/pod1234-abcd",
		},
		{
			name: "single slice without scope",
			path: cgroupPath{slices: []string{"kubepods"}},
			wantSystemd: "/kubepods.slice",
			wantCgroupfs: "/kubepods",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.path.ToSystemd(); got != tt.wantSystemd {
				t.Errorf("ToSystemd() = %q, want %q", got, tt.wantSystemd)
			}
			if got := tt.path.ToCgroupfs(); got != tt.wantCgroupfs {
				t.Errorf("ToCgroupfs() = %q, want %q", got, tt.wantCgroupfs)
			}
		})
	}
}

func TestExpandSystemdSlice(t *testing.T) {
	if got, want := expandSystemdSlice("a-b-c.slice"), "/a.slice/a-b.slice/a-b-c.slice"; got != want {
		t.Errorf("expandSystemdSlice() = %q, want %q", got, want)
	}
}
