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

func TestLatencyStageInfo(t *testing.T) {
	thresholds := []uint64{10, 20, 30}

	name, threshold, ok := latencyStageInfo(0, thresholds)
	if !ok || name != "RX_STAGE_NETIF" || threshold != 10 {
		t.Fatalf("stage 0 = (%q, %d, %v)", name, threshold, ok)
	}

	name, threshold, ok = latencyStageInfo(2, thresholds)
	if !ok || name != "RX_STAGE_USERCOPY" || threshold != 30 {
		t.Fatalf("stage 2 = (%q, %d, %v)", name, threshold, ok)
	}

	if _, _, ok = latencyStageInfo(3, thresholds); ok {
		t.Fatal("out-of-range stage should be rejected")
	}
	if _, _, ok = latencyStageInfo(-1, thresholds); ok {
		t.Fatal("negative stage should be rejected")
	}
	if _, _, ok = latencyStageInfo(0, nil); ok {
		t.Fatal("empty thresholds should be rejected")
	}
}
