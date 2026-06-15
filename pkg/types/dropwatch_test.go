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

package types

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/google/go-cmp/cmp"

	"huatuo-bamai/internal/packet"
)

// TestDropWatchTracingRoundTrip verifies the layered Packet survives a JSON
// round-trip without any tagged-union routing — `json.Unmarshal` sees a
// concrete *packet.Packet and restores it directly.
func TestDropWatchTracingRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		pkt  *packet.Packet
	}{
		{
			name: "ipv4_tcp",
			pkt: &packet.Packet{
				Ether: &packet.Ether{Src: mustMAC("aa:bb:cc:dd:ee:ff"), Dst: mustMAC("11:22:33:44:55:66"), Type: "IPv4"},
				IPv4:  &packet.IPv4{Src: net.IPv4(10, 0, 0, 1), Dst: net.IPv4(10, 0, 0, 2)},
				TCP: &packet.TCP{
					SrcPort: 1234, DstPort: 80, Seq: 1, Ack: 2, Window: 3,
					Flags: "FIN|ACK", SkState: "CLOSE_WAIT",
				},
			},
		},
		{
			name: "ipv6_udp",
			pkt: &packet.Packet{
				Ether: &packet.Ether{Src: mustMAC("aa:bb:cc:dd:ee:ff"), Dst: mustMAC("11:22:33:44:55:66"), Type: "IPv6"},
				IPv6:  &packet.IPv6{Src: net.ParseIP("::1"), Dst: net.ParseIP("::2")},
				UDP:   &packet.UDP{SrcPort: 53, DstPort: 1234, Length: 64, Checksum: 0xabcd},
			},
		},
		{
			name: "arp",
			pkt: &packet.Packet{
				Ether: &packet.Ether{Src: mustMAC("aa:bb:cc:dd:ee:ff"), Dst: mustMAC("11:22:33:44:55:66"), Type: "ARP"},
				ARP: &packet.ARP{
					Operation: "request",
					SenderMAC: mustMAC("aa:bb:cc:dd:ee:ff"), SenderIP: net.IPv4(10, 0, 0, 1),
					TargetMAC: mustMAC("00:00:00:00:00:00"), TargetIP: net.IPv4(10, 0, 0, 2),
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := &DropWatchTracing{
				ObservedTimestamp: "2026-06-13T00:00:00Z",
				Layers:            tc.pkt,
			}

			b, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var got DropWatchTracing
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if diff := cmp.Diff(tc.pkt, got.Layers); diff != "" {
				t.Errorf("Layers mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDropWatchTracingNilLayers(t *testing.T) {
	src := &DropWatchTracing{ObservedTimestamp: "2026-06-13T00:00:00Z"}

	b, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got DropWatchTracing
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Layers != nil {
		t.Errorf("Layers: want nil, got %v", got.Layers)
	}
}

func mustMAC(s string) string {
	if _, err := net.ParseMAC(s); err != nil {
		panic(err)
	}

	return s
}
