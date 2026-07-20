// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package procutil

import (
	"reflect"
	"testing"
)

func TestGroupProcessesByRoot(t *testing.T) {
	tests := []struct {
		name        string
		parentByPID map[int]int
		want        map[int][]int
	}{
		{
			name: "groups a complete matching ancestor chain under its root",
			parentByPID: map[int]int{
				100: 1,
				101: 100,
				102: 101,
				200: 1,
			},
			want: map[int][]int{
				100: {100, 101, 102},
				200: {200},
			},
		},
		{
			name: "uses a stable root for a cyclic parent graph",
			parentByPID: map[int]int{
				100: 102,
				101: 100,
				102: 101,
				200: 102,
			},
			want: map[int][]int{
				100: {100, 101, 102, 200},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := groupProcessesByRoot(tt.parentByPID); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("groupProcessesByRoot() = %v, want %v", got, tt.want)
			}
		})
	}
}
