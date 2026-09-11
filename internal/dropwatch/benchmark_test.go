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

package dropwatch

import (
	"fmt"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf/abi"
)

// The map stub returns owned bytes like ReadMap, but excludes kernel lookup cost.
func BenchmarkTracerReadStatus(b *testing.B) {
	for _, cpus := range []int{1, 8, 64, 256} {
		b.Run(fmt.Sprintf("cpus=%d", cpus), func(b *testing.B) {
			tracer := &Tracer{
				bpf: &tracerBPFStub{
					perfRaw: make([]byte, cpus*abi.BPFPerfOutputStatsSize),
					rateRaw: make([]byte, abi.BPFRatelimitEventSize),
				},
				perfStatusMap:     testDropwatchPerfStatusMapID,
				rateLimitStateMap: testDropwatchRateLimitMapID,
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := tracer.ReadStatus(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecodePacket(b *testing.B) {
	record := tcpRecord()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := DecodePacket(record); err != nil {
			b.Fatal(err)
		}
	}
}
