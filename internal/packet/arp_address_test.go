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
	"encoding/binary"
	"encoding/json"
	"testing"
)

// TestParseARPNonIPProtocolAddress: an ARP frame whose protocol address is not
// 4 or 16 bytes must still be JSON-marshalable. A 3-byte net.IP (plen=3) fails
// net.IP.MarshalText, which breaks the dropwatch JSON/socket writers.
func TestParseARPNonIPProtocolAddress(t *testing.T) {
	pkt := Hdr{EthProto: 0x0806, RawLen: 26}
	r := pkt.Raw[:]
	binary.BigEndian.PutUint16(r[0:], 1)      // htype Ethernet
	binary.BigEndian.PutUint16(r[2:], 0x0800) // ptype IPv4
	r[4] = 6                                  // hlen
	r[5] = 3                                  // plen (not a valid IP length)
	binary.BigEndian.PutUint16(r[6:], 1)      // request
	copy(r[8:14], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
	copy(r[14:17], []byte{10, 0, 0})
	copy(r[17:23], []byte{0, 0, 0, 0, 0, 0})
	copy(r[23:26], []byte{10, 0, 0})

	p, err := Parse(&pkt)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.ARP == nil {
		t.Fatalf("ARP: want non-nil, got nil")
	}
	if _, err := json.Marshal(p); err != nil {
		t.Fatalf("json.Marshal(plen=%d) error = %v", p.ARP.ProtAddressSize, err)
	}
}
