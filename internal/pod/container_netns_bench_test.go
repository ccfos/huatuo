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
)

func benchNetnsContainers(b *testing.B, n int) func() {
	previous := containerView
	containerView = newContainerStore()
	containerView.isActive, containerView.err = true, nil
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%04d", i)
		containerView.records[id] = &containerRecord{
			container: &Container{
				ID:                 id,
				Type:               ContainerTypeNormal,
				NetNamespaceCookie: uint64(1000 + i),
				NetNamespaceInum:   uint64(2000 + i),
				CgroupCss:          map[string]uint64{"memory": uint64(50 + i)},
			},
		}
	}
	return func() { containerView = previous }
}

func BenchmarkContainerByCSS(b *testing.B) {
	defer benchNetnsContainers(b, 100)()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ContainerByCSS(50, "memory"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerByCSSLinear(b *testing.B) {
	defer benchNetnsContainers(b, 100)()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		all, err := NormalContainers()
		if err != nil {
			b.Fatal(err)
		}
		for _, c := range all {
			if c.CgroupCss["memory"] == 50 {
				break
			}
		}
	}
}

func BenchmarkContainerByNetNamespaceCookie(b *testing.B) {
	defer benchNetnsContainers(b, 100)()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ContainerByNetNamespaceCookie(1050); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkContainerByNetNamespaceCookieLinear(b *testing.B) {
	defer benchNetnsContainers(b, 100)()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		all, err := NormalContainers()
		if err != nil {
			b.Fatal(err)
		}
		for _, c := range all {
			if c.NetNamespaceCookie == 1050 {
				break
			}
		}
	}
}
