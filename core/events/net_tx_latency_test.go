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

import "testing"

// TestTxStageNamesOrdering pins txStageNames to the bpf/net_tx_latency.c
// enum tx_lat_stage ordering (SENDMSG/NIC). The lookupLatStage bounds check in
// the TX decode loop relies on this slice being correctly sized.
func TestTxStageNamesOrdering(t *testing.T) {
	want := []string{"TX_STAGE_SENDMSG", "TX_STAGE_NIC"}
	if len(txStageNames) != len(want) {
		t.Fatalf("txStageNames len = %d, want %d (%v)", len(txStageNames), len(want), txStageNames)
	}
	for i := range want {
		if txStageNames[i] != want[i] {
			t.Fatalf("txStageNames[%d] = %q, want %q", i, txStageNames[i], want[i])
		}
	}
}

// TestLookupLatStageTXBounds exercises lookupLatStage against the TX stage set:
// every in-range stage resolves, and out-of-range indices return ok=false
// instead of panicking on the slice index.
func TestLookupLatStageTXBounds(t *testing.T) {
	thresholds := []uint64{50, 1}
	for i := 0; i < len(txStageNames); i++ {
		name, th, ok := lookupLatStage(uint8(i), txStageNames, thresholds)
		if !ok {
			t.Fatalf("stage %d should be in range", i)
		}
		if name != txStageNames[i] {
			t.Fatalf("stage %d name = %q, want %q", i, name, txStageNames[i])
		}
		if th != thresholds[i] {
			t.Fatalf("stage %d threshold = %d, want %d", i, th, thresholds[i])
		}
	}
	for _, bad := range []uint8{uint8(len(txStageNames)), uint8(len(txStageNames)) + 1, 0xFF} {
		if _, _, ok := lookupLatStage(bad, txStageNames, thresholds); ok {
			t.Fatalf("stage %d should be out of range", bad)
		}
	}
}
