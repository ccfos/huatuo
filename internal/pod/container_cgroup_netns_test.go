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
	"time"

	"huatuo-bamai/internal/cgroups/subsystem"
)

func TestContainerIDByCgroupNetNS(t *testing.T) {
	previousContainers := containers
	previousUpdatedAt := lastUpdatedAt

	containers = map[string]*Container{
		"css": {
			ID:        "css",
			Type:      ContainerTypeNormal,
			CgroupCss: map[string]uint64{subsystem.SubsystemMemory: 11},
		},
		"cookie": {
			ID:                 "cookie",
			Type:               ContainerTypeNormal,
			NetNamespaceCookie: 22,
		},
		"inum": {
			ID:               "inum",
			Type:             ContainerTypeNormal,
			NetNamespaceInum: 33,
		},
	}
	lastUpdatedAt = time.Now()
	t.Cleanup(func() {
		containers = previousContainers
		lastUpdatedAt = previousUpdatedAt
	})

	tests := []struct {
		name string
		ids  ContainerCgroupNetNS
		want string
	}{
		{
			name: "css takes precedence",
			ids: ContainerCgroupNetNS{
				MemoryCgroupCSSAddr: 11,
				NetNamespaceCookie:  22,
				NetNamespaceInum:    33,
			},
			want: "css",
		},
		{
			name: "net namespace cookie falls back after CSS miss",
			ids: ContainerCgroupNetNS{
				MemoryCgroupCSSAddr: 99,
				NetNamespaceCookie:  22,
				NetNamespaceInum:    33,
			},
			want: "cookie",
		},
		{
			name: "net namespace inum falls back after CSS and cookie misses",
			ids: ContainerCgroupNetNS{
				MemoryCgroupCSSAddr: 99,
				NetNamespaceCookie:  88,
				NetNamespaceInum:    33,
			},
			want: "inum",
		},
		{
			name: "no matching metadata",
			ids: ContainerCgroupNetNS{
				MemoryCgroupCSSAddr: 99,
				NetNamespaceCookie:  88,
				NetNamespaceInum:    77,
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerIDByCgroupNetNS(tt.ids)
			if got != tt.want {
				t.Errorf("ContainerIDByCgroupNetNS(%+v) = %q, want %q", tt.ids, got, tt.want)
			}
		})
	}
}
