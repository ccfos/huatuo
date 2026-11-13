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
	"strings"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func BenchmarkFindStack(b *testing.B) {
	for _, depth := range []int{5, 32, 64} {
		for _, text := range []string{"symbols", "files", "long"} {
			b.Run(fmt.Sprintf("depth=%d/%s", depth, text), func(b *testing.B) {
				input := benchmarkStackFrames("user", depth)
				for i := range input {
					if text != "symbols" {
						input[i].File = "/src/service/worker.go"
						input[i].Line = int64(i + 1)
					}

					if text == "long" {
						input[i].Name += strings.Repeat("namespace::", 32)
						input[i].File = strings.Repeat("/directory", 32) + input[i].File
					}
				}

				for _, hit := range []bool{false, true} {
					b.Run(fmt.Sprintf("hit=%t", hit), func(b *testing.B) {
						a := New()
						var want uint32
						if hit {
							_, hash := a.findStackID(input)
							want = a.insertStack(input, hash)
						}

						b.ReportAllocs()
						for b.Loop() {
							id, _ := a.findStackID(input)
							if id != want {
								b.Fatalf("stack ID = %d, want %d", id, want)
							}
						}
					})
				}
			})
		}
	}
}

func BenchmarkFindStackBoundaries(b *testing.B) {
	for _, size := range []int{127, 128, 129, 255, 256, 257} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			a := New()
			input := []types.ProfileFrame{{Name: strings.Repeat("x", size-24)}}
			_, hash := a.findStackID(input)
			want := a.insertStack(input, hash)
			b.ReportAllocs()
			for b.Loop() {
				id, _ := a.findStackID(input)
				if id != want {
					b.Fatalf("stack ID = %d, want %d", id, want)
				}
			}
		})
	}
}

func BenchmarkFindStackLargeText(b *testing.B) {
	for _, size := range []int{8 << 10, 128 << 10} {
		b.Run(fmt.Sprintf("text_bytes=%d", size), func(b *testing.B) {
			a := New()
			input := []types.ProfileFrame{{Name: strings.Repeat("x", size)}}
			_, hash := a.findStackID(input)
			want := a.insertStack(input, hash)
			b.ReportAllocs()
			for b.Loop() {
				id, _ := a.findStackID(input)
				if id != want {
					b.Fatalf("stack ID = %d, want %d", id, want)
				}
			}
		})
	}
}

func BenchmarkFindLabels(b *testing.B) {
	for _, count := range []int{1, 4, 16} {
		for _, kind := range []string{"string", "number", "long"} {
			b.Run(fmt.Sprintf("count=%d/%s", count, kind), func(b *testing.B) {
				labels := make([]types.ProfileLabel, count)
				for i := range labels {
					labels[i] = types.ProfileStringLabel(fmt.Sprintf("key%d", i), "worker")
					if kind == "number" {
						labels[i].Kind = types.ProfileLabelNumber
						labels[i].StringValue = ""
						labels[i].NumberValue = int64(i)
						labels[i].NumberUnit = "bytes"
					}
					if kind == "long" {
						labels[i].StringValue = strings.Repeat("value", 200)
					}
				}
				for _, hit := range []bool{false, true} {
					b.Run(fmt.Sprintf("hit=%t", hit), func(b *testing.B) {
						a := New()
						var want uint32
						if hit {
							_, hash := a.findLabelSetID(labels)
							want = a.insertLabelSet(labels, hash)
						}
						b.ReportAllocs()
						for b.Loop() {
							if id, _ := a.findLabelSetID(labels); id != want {
								b.Fatalf("label ID = %d, want %d", id, want)
							}
						}
					})
				}
			})
		}
	}
}
