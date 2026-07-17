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

package events

import (
	"strings"
	"testing"
)

// TestLatStageNamesProtocolAgnostic guards the RX_STAGE_TCPV4 -> RX_STAGE_TCP
// rename: the TCP stage name must be family-neutral because both AF_INET and
// AF_INET6 events surface it, with the family carried separately by
// address_family. No stage name may embed a V4/V6 token.
func TestLatStageNamesProtocolAgnostic(t *testing.T) {
	tcpFound := false
	for _, name := range latStageNames {
		if strings.Contains(name, "V4") || strings.Contains(name, "V6") {
			t.Fatalf("stage name %q embeds a family token", name)
		}
		if name == "RX_STAGE_TCP" {
			tcpFound = true
		}
	}
	if !tcpFound {
		t.Fatalf("RX_STAGE_TCP missing from latStageNames: %v", latStageNames)
	}
}

// TestLatStageNamesOrdering pins latStageNames to the bpf/net_rx_latency.c
// enum rx_lat_stage ordering (NETIF/TCP/USERCOPY) so a C-side enum change can't
// silently desync the Go slice and mislabel events or break the latThresholds
// lookup.
func TestLatStageNamesOrdering(t *testing.T) {
	want := []string{"RX_STAGE_NETIF", "RX_STAGE_TCP", "RX_STAGE_USERCOPY"}
	if len(latStageNames) != len(want) {
		t.Fatalf("latStageNames len = %d, want %d (%v)", len(latStageNames), len(want), latStageNames)
	}
	for i := range want {
		if latStageNames[i] != want[i] {
			t.Fatalf("latStageNames[%d] = %q, want %q", i, latStageNames[i], want[i])
		}
	}
}

// TestLookupLatStageRXBounds exercises lookupLatStage against the RX stage set:
// every in-range stage resolves, and out-of-range indices return ok=false
// instead of panicking on the slice index.
func TestLookupLatStageRXBounds(t *testing.T) {
	thresholds := []uint64{5, 10, 115}
	for i := 0; i < len(latStageNames); i++ {
		name, th, ok := lookupLatStage(uint8(i), latStageNames, thresholds)
		if !ok {
			t.Fatalf("stage %d should be in range", i)
		}
		if name != latStageNames[i] {
			t.Fatalf("stage %d name = %q, want %q", i, name, latStageNames[i])
		}
		if th != thresholds[i] {
			t.Fatalf("stage %d threshold = %d, want %d", i, th, thresholds[i])
		}
	}
	for _, bad := range []uint8{uint8(len(latStageNames)), uint8(len(latStageNames)) + 1, 0xFF} {
		if _, _, ok := lookupLatStage(bad, latStageNames, thresholds); ok {
			t.Fatalf("stage %d should be out of range", bad)
		}
	}
}

// TestLookupLatStageThresholdMismatch guards the fail-closed path: when the
// thresholds slice is shorter than the names slice, even a valid-name index must
// return ok=false rather than index out of range on thresholds.
func TestLookupLatStageThresholdMismatch(t *testing.T) {
	short := []uint64{1} // len 1 < len(latStageNames)
	for i := 1; i < len(latStageNames); i++ {
		if _, _, ok := lookupLatStage(uint8(i), latStageNames, short); ok {
			t.Fatalf("stage %d should fail closed with mismatched thresholds", i)
		}
	}
}
