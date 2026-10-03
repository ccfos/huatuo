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

package profiler

import "testing"

func TestBuildTreeItem(t *testing.T) {
	tests := []struct {
		name         string
		prefixes     []string
		userFrames   []string
		kernelFrames []string
		value        uint64
		want         []string
	}{
		{
			name:         "all frame groups",
			prefixes:     []string{"root", "label"},
			userFrames:   []string{"generic::<[u8; 7]>", "user.inner"},
			kernelFrames: []string{"kernel.outer", "kernel;frame"},
			value:        7,
			want:         []string{"root", "label", "generic::<[u8; 7]>", "user.inner", "kernel.outer", "kernel;frame"},
		},
		{
			name:         "no prefixes",
			userFrames:   []string{"user"},
			kernelFrames: []string{"kernel"},
			value:        3,
			want:         []string{"user", "kernel"},
		},
		{
			name:     "prefixes only",
			prefixes: []string{"root"},
			value:    1,
			want:     []string{"root"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := BuildTreeItem(test.prefixes, test.userFrames, test.kernelFrames, test.value)
			if item.Value != test.value {
				t.Fatalf("value = %d, want %d", item.Value, test.value)
			}
			if len(item.Stack) != len(test.want) {
				t.Fatalf("stack length = %d, want %d", len(item.Stack), len(test.want))
			}
			for index, frame := range item.Stack {
				if string(frame) != test.want[index] {
					t.Fatalf("stack[%d] = %q, want %q", index, frame, test.want[index])
				}
			}
		})
	}
}
