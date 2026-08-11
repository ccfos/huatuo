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

package qdisc

import (
	"testing"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

var benchmarkStats Stats

func BenchmarkDecodeMessagePacket64(b *testing.B) {
	stats2 := marshalAttributes(b, []netlink.Attribute{
		{Type: tcaStatsBasic, Data: basicStats(1_024, 767_358_010)},
		{Type: tcaStatsPacket64, Data: uint64Stats(9_357_292_602)},
	})
	message := qdiscMessage(b, 1, 0, []netlink.Attribute{
		{Type: unix.TCA_KIND, Data: []byte("mq\x00")},
		{Type: unix.TCA_STATS2, Data: stats2},
	})
	netdevNames := map[int]string{1: "eth0"}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stats, err := decodeMessage(message, netdevNames)
		if err != nil {
			b.Fatalf("decode qdisc message: %v", err)
		}
		benchmarkStats = stats
	}
}
