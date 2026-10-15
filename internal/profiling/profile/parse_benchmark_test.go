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

package profile

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func BenchmarkParseRawData(b *testing.B) {
	data, err := json.Marshal([]SampleOutput{{PID: 123, Output: strings.Repeat("process 123;main;work 10\n", 1000)}})
	if err != nil {
		b.Fatal(err)
	}
	input := &ParseInput{ProfileType: ProfileTypeCpuSample, Data: data, Opt: &ParseOption{SampleRate: 100}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := ParseRawData(b.Context(), input); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseTreeWindow(b *testing.B) {
	for _, size := range []int{1, 1000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			items := make([]*TreeItem, size)
			for i := range items {
				items[i] = BuildTreeItem([]string{"process"}, []string{"main", fmt.Sprintf("work%d", i)}, nil, 7)
			}
			b.ReportAllocs()
			for b.Loop() {
				data, err := ParseTree(time.Unix(100, 0), ProfileTypeCpuSample, items, &ParseOption{SampleRate: 100})
				if err != nil {
					b.Fatal(err)
				}
				_ = NewWindow(data, "container")
			}
		})
	}
}
