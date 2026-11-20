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

package formatter_test

import (
	"fmt"
	"path/filepath"
	"testing"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	"github.com/ccfos/huatuo/pkg/types"
)

// Measure the whole task, including deferred rendering and filesystem output.
// Positive CPU weights keep this workload comparable with the pre-live baseline.
func BenchmarkSummaryFormatter(b *testing.B) {
	for _, format := range []output.OutputFormat{output.FormatCollapsed, output.FormatSVG, output.FormatSpeedscope, output.FormatChromeTrace} {
		for _, workload := range []string{"repeated", "unique"} {
			b.Run(string(format)+"/"+workload, func(b *testing.B) {
				windows := summaryBenchmarkWindows(workload)
				if format == output.FormatChromeTrace {
					for i := range windows {
						for j := range windows[i].Aggregated.Samples {
							windows[i].Aggregated.Samples[j].Value = 1500
						}
					}
				}
				cfg := output.Config{
					Format:      format,
					Destination: filepath.Join(b.TempDir(), "profile"),
				}
				b.ReportAllocs()
				for b.Loop() {
					f, err := factory.NewFormatter(cfg)
					if err != nil {
						b.Fatal(err)
					}

					for i := range windows {
						if err := f.Add(b.Context(), &windows[i]); err != nil {
							b.Fatal(err)
						}
					}

					if err := f.Complete(b.Context()); err != nil {
						b.Fatal(err)
					}

					if err := f.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// 32 windows, 128 samples/window, 16 frames/stack; unique changes leaf identity
// each window while repeated keeps the same 128 identities for the whole task.
func summaryBenchmarkWindows(workload string) []types.AggregationWindow {
	windows := make([]types.AggregationWindow, 32)
	for i := range windows {
		w := &windows[i]
		w.ProfileType = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
		a := &w.Aggregated
		for depth := range 15 {
			a.Frames = append(a.Frames, types.ProfileFrame{Name: fmt.Sprintf("frame_%d", depth)})
		}

		for j := range 128 {
			id := j
			if workload == "unique" {
				id += i * 128
			}

			a.Frames = append(a.Frames, types.ProfileFrame{
				Name: fmt.Sprintf("leaf_%d", id),
				File: "worker.go",
			})
			stack := make([]uint32, 16)
			for depth := range 15 {
				stack[depth] = uint32(depth)
			}

			stack[15] = uint32(len(a.Frames) - 1)
			a.Stacks = append(a.Stacks, stack)
			a.Samples = append(a.Samples, types.AggregatedSample{
				UserStack: uint32(j + 1),
				Value:     1000000,
			})
		}
	}

	return windows
}
