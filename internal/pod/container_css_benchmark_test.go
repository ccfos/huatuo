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
	"fmt"
	"testing"
	"time"
)

func BenchmarkBuildCPURepresentatives(b *testing.B) {
	for _, kata := range []bool{false, true} {
		name := "runc"
		if kata {
			name = "kata"
		}
		b.Run(name, func(b *testing.B) {
			members := make(map[string]*Container, 1024)
			for i := range 1024 {
				c := &Container{ID: fmt.Sprint(i), CgroupCss: map[string]uint64{"cpu": uint64(i + 1)}, SyncedAt: time.Unix(int64(i), 0)}
				if kata {
					c.RuntimeType = "io.containerd.kata-qemu.v2"
					c.SandboxID = fmt.Sprint(i / 4)
					c.CgroupCss["cpu"] = uint64(i/4 + 1)
				}
				members[c.ID] = c
			}
			b.ReportAllocs()
			for b.Loop() {
				_ = BuildCssContainers(members, "cpu")
			}
		})
	}
}
