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

package aggregation

import (
	"fmt"
	"runtime"
	"strconv"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func benchmarkStackFrames(prefix string, depth int) []types.ProfileFrame {
	result := make([]types.ProfileFrame, depth)
	for i := range result {
		result[i].Name = prefix + "-frame-" + strconv.Itoa(i)
	}

	return result
}

func BenchmarkAggregatorAdd(b *testing.B) {
	for _, depth := range []int{5, 32, 64} {
		b.Run(fmt.Sprintf("repeated/user=%d", depth), func(b *testing.B) {
			p := New()
			s := Sample{
				UserFrames: benchmarkStackFrames("user", depth),
				Labels:     []types.ProfileLabel{types.ProfileStringLabel("comm", "worker"), types.ProfilePIDLabel(123)},
				Value:      1,
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := p.Add(&s); err != nil {
					b.Fatal(err)
				}
			}

			runtime.KeepAlive(p)
		})
	}

	b.Run("repeated/mixed=32+32", func(b *testing.B) {
		p := New()
		s := Sample{
			UserFrames:   benchmarkStackFrames("user", 32),
			KernelFrames: benchmarkStackFrames("kernel", 32),
			Value:        1,
		}
		b.ReportAllocs()
		for b.Loop() {
			if err := p.Add(&s); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("unique/user=32/batch=1024", func(b *testing.B) {
		samples := make([]Sample, 1024)
		for i := range samples {
			samples[i] = Sample{
				UserFrames: benchmarkStackFrames(strconv.Itoa(i), 32),
				Value:      1,
			}
		}

		b.ReportAllocs()
		for b.Loop() {
			p := New()
			for i := range samples {
				if err := p.Add(&samples[i]); err != nil {
					b.Fatal(err)
				}
			}

			runtime.KeepAlive(p)
		}
	})
}

func BenchmarkAggregatorWindows(b *testing.B) {
	for _, count := range []int{0, 1, 1024, 8192} {
		b.Run(fmt.Sprintf("identities=%d", count), func(b *testing.B) {
			samples := make([]Sample, count)
			for i := range samples {
				samples[i] = Sample{
					UserFrames: benchmarkStackFrames(strconv.Itoa(i), 5),
					Labels:     []types.ProfileLabel{types.ProfileStringLabel("key", strconv.Itoa(i))},
					Value:      1,
				}
			}
			a := New()
			b.ReportAllocs()
			for b.Loop() {
				for i := range samples {
					if err := a.Add(&samples[i]); err != nil {
						b.Fatal(err)
					}
				}
				result := a.Drain()
				runtime.KeepAlive(result)
			}
			runtime.KeepAlive(a)
		})
	}
}

// Measure live index storage after dropping the result, independently of input
// fixtures. GC noise makes this a retention estimate, not a per-map byte count.
func BenchmarkAggregatorRetainedIndexes(b *testing.B) {
	for _, count := range []int{1024, 8192} {
		b.Run(fmt.Sprintf("identities=%d", count), func(b *testing.B) {
			samples := make([]Sample, count)
			for i := range samples {
				samples[i] = Sample{
					UserFrames: benchmarkStackFrames(strconv.Itoa(i), 1),
					Labels:     []types.ProfileLabel{types.ProfileStringLabel("key", strconv.Itoa(i))},
					Value:      1,
				}
			}
			var retained int64
			for b.Loop() {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				a := New()
				for i := range samples {
					if err := a.Add(&samples[i]); err != nil {
						b.Fatal(err)
					}
				}
				a.Drain()
				runtime.GC()
				runtime.ReadMemStats(&after)
				retained += int64(after.HeapAlloc) - int64(before.HeapAlloc)
				runtime.KeepAlive(a)
			}
			runtime.KeepAlive(samples)
			b.ReportMetric(float64(retained)/float64(b.N), "retained-B/op")
		})
	}
}

func BenchmarkAggregatorNewKernel(b *testing.B) {
	for _, mode := range []string{"kernel", "distinct", "same"} {
		b.Run(mode, func(b *testing.B) {
			samples := make([]Sample, 256)
			for i := range samples {
				samples[i].KernelFrames = benchmarkStackFrames(strconv.Itoa(i), 32)
				samples[i].Value = 1
				switch mode {
				case "distinct":
					samples[i].UserFrames = benchmarkStackFrames("user"+strconv.Itoa(i), 32)
				case "same":
					samples[i].UserFrames = append([]types.ProfileFrame(nil), samples[i].KernelFrames...)
				}
			}
			a := New()
			b.ReportAllocs()
			for b.Loop() {
				for i := range samples {
					if err := a.Add(&samples[i]); err != nil {
						b.Fatal(err)
					}
				}
				result := a.Drain()
				runtime.KeepAlive(result)
			}
		})
	}
}

func BenchmarkAggregatorCopyProfile(b *testing.B) {
	a := New()
	for i := range 1024 {
		add(b, a, &Sample{
			UserFrames: frames("main", strconv.Itoa(i)),
			Labels:     []types.ProfileLabel{types.ProfilePIDLabel(uint32(i))},
			Value:      4096,
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		profile := a.CopyProfile()
		if len(profile.Samples) != 1024 {
			b.Fatal(len(profile.Samples))
		}
	}
}
