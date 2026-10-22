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

package speedscope

import (
	"fmt"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func BenchmarkAppendProfiles(b *testing.B) {
	for _, labelCount := range []int{4, 1024} {
		b.Run(fmt.Sprintf("labels=%d", labelCount), func(b *testing.B) {
			w := types.AggregationWindow{
				ContainerID: "container",
				ProfileType: "memory:physical_usage:bytes:space:bytes",
			}
			for i := range labelCount {
				w.Aggregated.LabelSets = append(w.Aggregated.LabelSets, []types.ProfileLabel{types.ProfilePIDLabel(uint32(i + 1))})
			}
			for i := range 1024 {
				w.Aggregated.Frames = append(w.Aggregated.Frames, types.ProfileFrame{Name: fmt.Sprintf("work%d", i)})
				w.Aggregated.Stacks = append(w.Aggregated.Stacks, []uint32{uint32(i)})
				w.Aggregated.Samples = append(w.Aggregated.Samples, types.AggregatedSample{
					UserStack: uint32(i + 1),
					LabelSet:  uint32(i%labelCount + 1),
					Value:     1,
				})
			}
			b.ReportAllocs()
			for b.Loop() {
				f := encoder{
					unit: "bytes",
				}
				if err := f.appendProfiles(b.Context(), &w); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAppendSharedFrames(b *testing.B) {
	for _, labelCount := range []int{4, 1024} {
		for _, kernel := range []bool{false, true} {
			b.Run(fmt.Sprintf("labels=%d/kernel=%t", labelCount, kernel), func(b *testing.B) {
				w := types.AggregationWindow{
					ContainerID: "container",
					ProfileType: "memory:physical_usage:bytes:space:bytes",
				}
				a := &w.Aggregated
				for i := range labelCount {
					a.LabelSets = append(a.LabelSets, []types.ProfileLabel{types.ProfilePIDLabel(uint32(i + 1))})
				}
				for i := range 31 {
					a.Frames = append(a.Frames, types.ProfileFrame{Name: fmt.Sprintf("shared%d", i)})
				}
				for i := range 1024 {
					a.Frames = append(a.Frames, types.ProfileFrame{Name: fmt.Sprintf("leaf%d", i)})
					stack := make([]uint32, 32)
					for j := range 31 {
						stack[j] = uint32(j)
					}
					stack[31] = uint32(len(a.Frames) - 1)
					a.Stacks = append(a.Stacks, stack)
					sample := types.AggregatedSample{
						UserStack: uint32(i + 1),
						LabelSet:  uint32(i%labelCount + 1),
						Value:     1,
					}
					if kernel {
						sample.KernelStack = 1
					}
					a.Samples = append(a.Samples, sample)
				}
				b.ReportAllocs()
				for b.Loop() {
					e := encoder{
						unit: "bytes",
					}
					if err := e.appendProfiles(b.Context(), &w); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
