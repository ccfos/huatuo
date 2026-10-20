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
	"testing"
)

// TestContainerMemoryCgroupPath 验证 Host 兼容路径、Guest 独立路径、统一路径，
// 以及 Guest 缺少 memory 控制器时不会误用 CPU 路径。
func TestContainerMemoryCgroupPath(t *testing.T) {
	for _, tc := range []struct {
		name, memory, want string
		guest              bool
	}{
		{name: "host-default", want: "/cpu/workload"},
		{name: "guest-split", memory: "/memory/workload", want: "/memory/workload", guest: true},
		{name: "guest-unified", memory: "/cpu/workload", want: "/cpu/workload", guest: true},
		{name: "guest-no-memory", guest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container := &Container{CgroupPath: "/cpu/workload"}
			if tc.guest {
				container.memoryCgroupPath = &tc.memory
			}
			if got := container.MemoryCgroupPath(); got != tc.want {
				t.Fatalf("memory path=%q, want %q", got, tc.want)
			}
		})
	}
}
