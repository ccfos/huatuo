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

package flamegraph

import (
	"fmt"
	"testing"
)

func benchmarkStacks(names int, depth int) []Stack {
	stacks := make([]Stack, 0, names)
	for i := range names {
		frames := make([]string, depth)
		for d := range depth {
			frames[d] = fmt.Sprintf("module_%d/func_%d_%d", i%64, d, i%97)
		}
		stacks = append(stacks, Stack{Names: frames, Samples: 1})
	}
	return stacks
}

func BenchmarkProcess10kStacks(b *testing.B) {
	stacks := benchmarkStacks(10000, 20)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var proc processor
		for i := range stacks {
			proc.Process(stacks[i])
		}
	}
}
