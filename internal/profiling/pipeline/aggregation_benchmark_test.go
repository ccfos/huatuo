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

package pipeline

import (
	"fmt"
	"runtime"
	"strconv"
	"testing"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	"github.com/ccfos/huatuo/pkg/types"
)

// Keep pipeline construction in this workload for comparison with the former
// BenchmarkNativeAggregatorAggregate, which included its queue and task ID.
func BenchmarkPipelineAdd(b *testing.B) {
	for _, depth := range []int{5, 32, 64} {
		b.Run(fmt.Sprintf("repeated/user=%d", depth), func(b *testing.B) {
			p := aggregationPipeline()
			s := aggregation.Sample{
				UserFrames: benchmarkStackFrames("user", depth),
				Labels:     []types.ProfileLabel{types.ProfileStringLabel("comm", "worker"), types.ProfilePIDLabel(123)},
				Value:      1,
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := p.aggregator.Add(&s); err != nil {
					b.Fatal(err)
				}
			}

			runtime.KeepAlive(p)
		})
	}

	b.Run("repeated/mixed=32+32", func(b *testing.B) {
		p := aggregationPipeline()
		s := aggregation.Sample{
			UserFrames:   benchmarkStackFrames("user", 32),
			KernelFrames: benchmarkStackFrames("kernel", 32),
			Value:        1,
		}
		b.ReportAllocs()
		for b.Loop() {
			if err := p.aggregator.Add(&s); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("unique/user=32/batch=1024", func(b *testing.B) {
		samples := make([]aggregation.Sample, 1024)
		for i := range samples {
			samples[i] = aggregation.Sample{
				UserFrames: benchmarkStackFrames(strconv.Itoa(i), 32),
				Value:      1,
			}
		}

		b.ReportAllocs()
		for b.Loop() {
			p := aggregationPipeline()
			for i := range samples {
				if err := p.aggregator.Add(&samples[i]); err != nil {
					b.Fatal(err)
				}
			}

			runtime.KeepAlive(p)
		}
	})
}
