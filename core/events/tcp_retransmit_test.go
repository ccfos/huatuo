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
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func TestHandleTCPRetransmitEventPreservesCorrelationResult(t *testing.T) {
	perfStatus := &types.DropwatchPerfStatus{PerfLost: 1}
	event := &types.TCPRetransmitTracing{
		ObservedTimestamp: "2026-09-10T08:00:00Z",
		ContainerID:       "container-id",
		DropLocation:      "unknown",
		CorrelationReasons: []types.CorrelationReason{
			types.CorrelationReasonStartupHistoryIncomplete,
		},
		DropwatchPerfStatus: perfStatus,
		DropStack:           "kfree_skb/1",
	}
	if err := handleTCPRetransmitEvent(nil, event); err != nil {
		t.Fatal(err)
	}
	if event.DropLocation != "unknown" {
		t.Fatalf("DropLocation = %q, want finalized result unchanged", event.DropLocation)
	}
	if event.DropwatchPerfStatus != perfStatus {
		t.Fatal("DropwatchPerfStatus changed while saving finalized result")
	}
	if len(event.CorrelationReasons) != 1 ||
		event.CorrelationReasons[0] != types.CorrelationReasonStartupHistoryIncomplete {
		t.Fatalf("CorrelationReasons = %v, want finalized reasons unchanged", event.CorrelationReasons)
	}
	if event.DropStack != "kfree_skb/1" {
		t.Fatalf("DropStack = %q, want finalized stack unchanged", event.DropStack)
	}
}
