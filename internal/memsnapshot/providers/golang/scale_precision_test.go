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

package golang

import (
	"fmt"
	"testing"
)

func TestScaleHeapSmallProbability(t *testing.T) {
	// For these ratios, the correction's higher-order terms are below one
	// integer ULP. Powers of two make the leading estimates exactly representable.
	for _, rate := range []int64{1 << 58, 1 << 60} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			objects, bytes := scaleHeapSample(1, 16, rate)
			if objects != rate/16 || bytes != rate {
				t.Fatalf("scaled = (%d, %d), want (%d, %d)", objects, bytes, rate/16, rate)
			}
		})
	}
}

var benchmarkScaledObjects, benchmarkScaledBytes int64

func BenchmarkScaleHeapSample(b *testing.B) {
	for _, rate := range []int64{1, 512 << 10, 1 << 58} {
		b.Run(fmt.Sprint(rate), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchmarkScaledObjects, benchmarkScaledBytes = scaleHeapSample(1, 16, rate)
			}
		})
	}
}
