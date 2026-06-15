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

package packet

import (
	"encoding/json"
	"net"
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestPacketJSONNested asserts that Packet marshals to a nested object whose
// top-level keys are the active layer names — so downstream ES documents have
// a per-layer sub-document instead of prefix-keyed flat fields.
func TestPacketJSONNested(t *testing.T) {
	cases := []struct {
		name     string
		pkt      *Packet
		wantKeys []string // top-level keys, sorted
	}{
		{
			name: "ipv4_tcp",
			pkt: &Packet{
				Ether: &Ether{Src: mac("aa:bb:cc:dd:ee:ff"), Dst: mac("11:22:33:44:55:66"), Type: "IPv4"},
				IPv4:  &IPv4{Src: net.IPv4(10, 0, 0, 1), Dst: net.IPv4(10, 0, 0, 2)},
				TCP:   &TCP{SrcPort: 1234, DstPort: 80, Flags: "SYN", SkState: "ESTABLISHED"},
			},
			wantKeys: []string{"ether", "ipv4", "tcp"},
		},
		{
			name: "ipv6_udp",
			pkt: &Packet{
				IPv6: &IPv6{Src: net.ParseIP("::1"), Dst: net.ParseIP("::2")},
				UDP:  &UDP{SrcPort: 53, DstPort: 1234, Length: 64, Checksum: 0xabcd},
			},
			wantKeys: []string{"ipv6", "udp"},
		},
		{
			name: "arp",
			pkt: &Packet{
				ARP: &ARP{
					Operation: "request",
					SenderMAC: mac("aa:bb:cc:dd:ee:ff"), SenderIP: net.IPv4(10, 0, 0, 1),
					TargetMAC: mac("00:00:00:00:00:00"), TargetIP: net.IPv4(10, 0, 0, 2),
				},
			},
			wantKeys: []string{"arp"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.pkt)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var got map[string]json.RawMessage
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			gotKeys := make([]string, 0, len(got))
			for k := range got {
				gotKeys = append(gotKeys, k)
			}
			sort.Strings(gotKeys)

			if diff := cmp.Diff(tc.wantKeys, gotKeys); diff != "" {
				t.Errorf("top-level keys mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPacketJSONRoundTrip exercises end-to-end marshal+unmarshal so that
// consumers can rely on `json.Unmarshal(b, &Packet{})` without any tagged-
// union dispatch.
func TestPacketJSONRoundTrip(t *testing.T) {
	cases := []*Packet{
		{
			Ether: &Ether{Src: mac("aa:bb:cc:dd:ee:ff"), Dst: mac("11:22:33:44:55:66"), Type: "IPv4"},
			IPv4:  &IPv4{Src: net.IPv4(10, 0, 0, 1), Dst: net.IPv4(10, 0, 0, 2)},
			TCP: &TCP{
				SrcPort: 1234, DstPort: 80, Seq: 1, Ack: 2, Window: 3,
				Flags: "SYN|ACK", SkState: "ESTABLISHED",
			},
		},
		{
			IPv6: &IPv6{Src: net.ParseIP("::1"), Dst: net.ParseIP("::2")},
			UDP:  &UDP{SrcPort: 53, DstPort: 1234, Length: 64, Checksum: 0xabcd},
		},
		{
			ARP: &ARP{
				Operation: "reply",
				SenderMAC: mac("aa:bb:cc:dd:ee:ff"), SenderIP: net.IPv4(10, 0, 0, 1),
				TargetMAC: mac("11:22:33:44:55:66"), TargetIP: net.IPv4(10, 0, 0, 2),
			},
		},
	}

	for _, want := range cases {
		b, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		var got Packet
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}

		if diff := cmp.Diff(want, &got); diff != "" {
			t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
		}
	}
}

// TestPacketJSONMACWireFormat pins MAC fields to the canonical
// "aa:bb:cc:dd:ee:ff" literal on the JSON wire. Regression guard: if the
// field types ever revert to net.HardwareAddr (or any []byte alias without
// MarshalText), encoding/json silently base64-encodes them. The round-trip
// test above doesn't catch that because the symmetric base64 decode produces
// identical bytes; only an external-format assertion exposes the drift.
func TestPacketJSONMACWireFormat(t *testing.T) {
	cases := []struct {
		name string
		pkt  *Packet
		want []string // substrings that must appear in the marshaled JSON
	}{
		{
			name: "ether",
			pkt: &Packet{
				Ether: &Ether{Src: "aa:bb:cc:dd:ee:ff", Dst: "11:22:33:44:55:66", Type: "IPv4"},
			},
			want: []string{
				`"src":"aa:bb:cc:dd:ee:ff"`,
				`"dst":"11:22:33:44:55:66"`,
			},
		},
		{
			name: "arp",
			pkt: &Packet{
				ARP: &ARP{
					Operation: "reply",
					SenderMAC: "aa:bb:cc:dd:ee:ff", SenderIP: net.IPv4(10, 0, 0, 1),
					TargetMAC: "11:22:33:44:55:66", TargetIP: net.IPv4(10, 0, 0, 2),
				},
			},
			want: []string{
				`"sender_mac":"aa:bb:cc:dd:ee:ff"`,
				`"target_mac":"11:22:33:44:55:66"`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.pkt)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(b), w) {
					t.Errorf("missing %s in JSON wire output: %s", w, b)
				}
			}
		})
	}
}

// TestPacketLabel asserts the Label() string for each protocol combination,
// since downstream code uses it as the protocol-combination tag.
func TestPacketLabel(t *testing.T) {
	cases := []struct {
		name string
		pkt  *Packet
		want string
	}{
		{"nil", nil, "unknown"},
		{"empty", &Packet{}, "unknown"},
		{"ipv4_tcp", &Packet{IPv4: &IPv4{}, TCP: &TCP{}}, "IPv4/TCP"},
		{"ipv6_tcp", &Packet{IPv6: &IPv6{}, TCP: &TCP{}}, "IPv6/TCP"},
		{"ipv4_udp", &Packet{IPv4: &IPv4{}, UDP: &UDP{}}, "IPv4/UDP"},
		{"ipv6_udp", &Packet{IPv6: &IPv6{}, UDP: &UDP{}}, "IPv6/UDP"},
		{"ipv4_icmp", &Packet{IPv4: &IPv4{}, ICMP: &ICMP{}}, "IPv4/ICMP"},
		{"ipv6_icmp", &Packet{IPv6: &IPv6{}, ICMP: &ICMP{}}, "IPv6/ICMPv6"},
		{"arp", &Packet{ARP: &ARP{}}, "ARP"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pkt.Label(); got != tc.want {
				t.Errorf("Label = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPacketString spot-checks the rendered format for each protocol — the
// dropwatch text writer concatenates this verbatim, so format drift would
// quietly change user-facing output.
func TestPacketString(t *testing.T) {
	cases := []struct {
		name      string
		pkt       *Packet
		wantParts []string // all must appear in the rendered string
	}{
		{
			name: "ipv4_tcp",
			pkt: &Packet{
				Ether: &Ether{Src: mac("aa:bb:cc:dd:ee:ff"), Dst: mac("11:22:33:44:55:66"), Type: "IPv4"},
				IPv4:  &IPv4{Src: net.IPv4(10, 0, 0, 1), Dst: net.IPv4(10, 0, 0, 2)},
				TCP:   &TCP{SrcPort: 1234, DstPort: 80, Flags: "SYN", SkState: "ESTABLISHED"},
			},
			wantParts: []string{
				"IPv4/TCP", "10.0.0.1:1234", "10.0.0.2:80", "[SYN]",
				"sk=ESTABLISHED", "smac=aa:bb:cc:dd:ee:ff", "dmac=11:22:33:44:55:66",
			},
		},
		{
			name: "ipv6_udp",
			pkt: &Packet{
				IPv6: &IPv6{Src: net.ParseIP("::1"), Dst: net.ParseIP("::2")},
				UDP:  &UDP{SrcPort: 53, DstPort: 1234, Length: 64, Checksum: 0xabcd},
			},
			wantParts: []string{"IPv6/UDP", "[::1]:53", "[::2]:1234", "len=64", "chk=0xabcd"},
		},
		{
			name: "arp",
			pkt: &Packet{
				ARP: &ARP{
					Operation: "request",
					SenderMAC: mac("aa:bb:cc:dd:ee:ff"), SenderIP: net.IPv4(10, 0, 0, 1),
					TargetMAC: mac("00:00:00:00:00:00"), TargetIP: net.IPv4(10, 0, 0, 2),
				},
			},
			wantParts: []string{
				"ARP", "request",
				"sender=10.0.0.1/aa:bb:cc:dd:ee:ff",
				"target=10.0.0.2/00:00:00:00:00:00",
			},
		},
		{
			name:      "ipv4_only",
			pkt:       &Packet{IPv4: &IPv4{Src: net.IPv4(10, 0, 0, 1), Dst: net.IPv4(10, 0, 0, 2)}},
			wantParts: []string{"IPv4", "10.0.0.1 > 10.0.0.2"},
		},
		{
			name:      "ipv6_only",
			pkt:       &Packet{IPv6: &IPv6{Src: net.ParseIP("::1"), Dst: net.ParseIP("::2")}},
			wantParts: []string{"IPv6", "::1 > ::2"},
		},
		{"nil_packet", nil, []string{"unknown"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.pkt.String()
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("String() = %q\n  missing %q", got, want)
				}
			}
		})
	}
}

func mac(s string) string {
	if _, err := net.ParseMAC(s); err != nil {
		panic(err)
	}

	return s
}
