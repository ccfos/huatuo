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

package collector

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestHwDropDelta(t *testing.T) {
	cases := []struct {
		name string
		prev uint64
		cur  uint64
		want uint64
	}{
		{"normal growth", 10, 15, 5},
		{"no change", 42, 42, 0},
		{"both zero", 0, 0, 0},
		{"first sample style", 0, 100, 100}, // pure-fn behavior; emitHwDropDelta suppresses via baseline
		{"counter reset", 1000, 5, 5},       // decrease reports current, not a spurious huge delta
		{"wraparound to zero", math.MaxUint64, 0, 0},
		{"wraparound small", math.MaxUint64 - 3, 2, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hwDropDelta(tc.prev, tc.cur)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("hwDropDelta(%d, %d) (-want +got):\n%s", tc.prev, tc.cur, diff)
			}
		})
	}
}
