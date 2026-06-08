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
	"testing"
)

func TestPacketTypeJSONRoundTrip(t *testing.T) {
	cases := []PacketType{
		PacketTypeUnknown,
		PacketTypeIPv4TCP,
		PacketTypeIPv4UDP,
		PacketTypeIPv4ICMP,
		PacketTypeIPv6TCP,
		PacketTypeIPv6UDP,
		PacketTypeIPv6ICMPv6,
		PacketTypeARP,
	}

	for _, pt := range cases {
		b, err := json.Marshal(pt)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", pt, err)
		}

		var got PacketType
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", b, err)
		}

		if got != pt {
			t.Errorf("round-trip %v: got %v", pt, got)
		}
	}
}

func TestPacketTypeUnmarshalUnknown(t *testing.T) {
	var pt PacketType
	if err := json.Unmarshal([]byte(`"no-such-protocol"`), &pt); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pt != PacketTypeUnknown {
		t.Errorf("want PacketTypeUnknown, got %v", pt)
	}
}
