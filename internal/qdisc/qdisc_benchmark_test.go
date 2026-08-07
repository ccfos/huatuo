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
	"github.com/mdlayher/netlink/nlenc"
)

var benchmarkInfo Info

func BenchmarkParseMessagePacket64(b *testing.B) {
	basic := make([]byte, 12)
	nlenc.PutUint64(basic[0:8], 1_024)
	nlenc.PutUint32(basic[8:12], 767_358_010)
	packet64 := make([]byte, 8)
	nlenc.PutUint64(packet64, 9_357_292_602)
	stats, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaStatsBasic, Data: basic},
		{Type: tcaStatsPacket64, Data: packet64},
	})
	if err != nil {
		b.Fatalf("marshal qdisc statistics: %v", err)
	}
	attrs, err := netlink.MarshalAttributes([]netlink.Attribute{
		{Type: tcaKind, Data: []byte("mq\x00")},
		{Type: tcaStats2, Data: stats},
	})
	if err != nil {
		b.Fatalf("marshal qdisc attributes: %v", err)
	}
	data := make([]byte, 20+len(attrs))
	nlenc.PutUint32(data[4:8], 1)
	copy(data[20:], attrs)
	message := netlink.Message{Data: data}
	ifaceNames := map[int]string{1: "eth0"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		info, err := parseMessage(message, ifaceNames)
		if err != nil {
			b.Fatalf("parse qdisc message: %v", err)
		}
		benchmarkInfo = info
	}
}
