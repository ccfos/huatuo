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
)

func TestDecodeStats2Packet64WithPadding(t *testing.T) {
	const padAttribute = 6 // TCA_STATS_PAD in Linux gen_stats.h.
	const wantPackets = uint64(1<<32 + 123)
	tests := []struct {
		name       string
		attributes []netlink.Attribute
		want       uint64
	}{
		{
			name: "software packet counter after alignment padding",
			attributes: []netlink.Attribute{
				{Type: tcaStatsBasic, Data: append(basicStats(456, 123), make([]byte, 4)...)},
				{Type: padAttribute},
				{Type: tcaStatsPacket64, Data: uint64Stats(wantPackets)},
			},
			want: wantPackets,
		},
		{
			name: "hardware packet counter remains separate",
			attributes: []netlink.Attribute{
				{Type: tcaStatsBasic, Data: basicStats(456, 123)},
				{Type: tcaStatsBasicHardware, Data: basicStats(789, 42)},
				{Type: padAttribute},
				{Type: tcaStatsPacket64, Data: uint64Stats(wantPackets)},
			},
			want: 123,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeStats2(marshalAttributes(t, tt.attributes))
			if err != nil {
				t.Fatal(err)
			}
			if got.packets != tt.want {
				t.Fatalf("packets = %d, want %d", got.packets, tt.want)
			}
			if got.bytes != 456 {
				t.Fatalf("bytes = %d, want 456", got.bytes)
			}
		})
	}
}
