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

	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

func seedCSSFixture(n int) func() {
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%04d", i)
		for sub, base := range map[string]uint64{
			subsystem.SubsystemMemory: uint64(1000+i) * 10,
		} {
			m := &containerCssMetaData{CSS: base, SubSys: sub, ContainerID: id}
			cgroupCssMetaDataMap.Store(base, m)
			addrsAny, _ := cgroupCssByContainerID.LoadOrStore(id, &map[uint64]struct{}{})
			(*addrsAny.(*map[uint64]struct{}))[base] = struct{}{}
		}
	}
	return func() {
		cgroupCssMetaDataMap.Range(func(k, _ any) bool {
			cgroupCssMetaDataMap.Delete(k)
			return true
		})
		cgroupCssByContainerID.Range(func(k, _ any) bool {
			cgroupCssByContainerID.Delete(k)
			return true
		})
	}
}

func TestCgroupListCssDataByKnodeIndexed(t *testing.T) {
	cleanup := seedCSSFixture(3)
	defer cleanup()

	got := cgroupListCssDataByKnode("c0001")
	if len(got) != 1 || got[0].SubSys != subsystem.SubsystemMemory {
		t.Fatalf("unexpected css list: %+v", got)
	}
	if len(cgroupListCssDataByKnode("missing")) != 0 {
		t.Fatal("expected empty list for unknown container")
	}
}

func BenchmarkCgroupListCssDataByKnode(b *testing.B) {
	const n = 100
	cleanup := seedCSSFixture(n)
	defer cleanup()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cgroupListCssDataByKnode(fmt.Sprintf("c%04d", i%n))
	}
}

func BenchmarkCgroupListCssDataByKnodeRange(b *testing.B) {
	const n = 100
	cleanup := seedCSSFixture(n)
	defer cleanup()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		containerID := fmt.Sprintf("c%04d", i%n)
		res := []*containerCssMetaData{}
		cgroupCssMetaDataMap.Range(func(k, v any) bool {
			if m, ok := v.(*containerCssMetaData); ok && m.ContainerID == containerID {
				res = append(res, m)
			}
			return true
		})
	}
}
