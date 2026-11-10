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

package autotracing

import "testing"

func BenchmarkDecodeSchedBlameBatch(b *testing.B) {
	packedSlices := make([]schedBlamePackedSlice, schedBlameMaxBatchSlices)
	for index := range packedSlices {
		packedSlices[index] = packSchedBlameSliceForTest(
			1_000,
			1<<uint(index%schedBlameBaseBitmapBits),
			uint16(index),
		)
		packedSlices[index].BitmapExtra[0] = 1 << uint(index%64)
	}
	raw := encodeSchedBlameBatchForTest(packedSlices, 0)
	record := new(schedBlameRecord)

	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for range b.N {
		if err := decodeSchedBlameRecord(raw, record); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSchedBlameHandlePackedSlice(b *testing.B) {
	const (
		targetCSSAddr     = 0x100
		competitorCSSAddr = 0x200
		targetCSSID       = 65
		competitorCSSID   = 1057
	)
	state := newSchedBlameStateForTest(0, targetCSSAddr)
	state.handleIdentity(&schedBlameIdentity{
		CSSID:    targetCSSID,
		CSSAddr:  targetCSSAddr,
		KernfsID: 1,
	})
	state.handleIdentity(&schedBlameIdentity{
		CSSID:    competitorCSSID,
		CSSAddr:  competitorCSSAddr,
		KernfsID: 1,
	})
	packedSlice := packSchedBlameSliceForTest(
		1_000,
		1<<schedBlameHighlightIndex,
		competitorCSSID,
	)
	packedSlice.BitmapExtra[0] = 1

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		state.handlePackedSlice(&packedSlice)
	}
}
