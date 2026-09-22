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

package retransmit

import (
	"math"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestCorrelatorMatchAndRemoveDropBothFlowDirections(t *testing.T) {
	tests := []struct {
		name string
		drop *dropEvent
	}{
		{
			name: "outbound segment",
			drop: testDropEvent(
				t,
				1,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
				0,
				packet.TCPFlagACK|packet.TCPFlagPSH,
			),
		},
		{
			name: "reverse ACK",
			drop: testDropEvent(
				t,
				1,
				"10.0.0.2",
				"10.0.0.1",
				80,
				1000,
				900,
				900,
				200,
				packet.TCPFlagACK,
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Unix(1, 0)
			correlator := newTestEventCorrelator(t, 1)
			correlator.dropStore.add(&storeEntry[*dropEvent]{value: test.drop}, &test.drop.flow, now)
			retransmit := testRetransmitEvent(
				2,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
			)
			match, ok := retransmitEntryFromEvent(retransmit)
			if !ok {
				t.Fatal("retransmitEntryFromEvent() = false")
			}
			got, hasCrossNetNSCandidate := correlator.matchAndRemoveDrop(&match)
			if got != test.drop || hasCrossNetNSCandidate {
				t.Fatalf(
					"matchAndRemoveDrop() = (%p, %t), want (%p, false)",
					got,
					hasCrossNetNSCandidate,
					test.drop,
				)
			}
		})
	}
}

func TestCorrelatorMatchAndRemoveDropEnforcesCausalAge(t *testing.T) {
	const dropKtimeNS = uint64(time.Second)
	tests := []struct {
		name            string
		retransmitKtime uint64
		shouldMatch     bool
	}{
		{
			name:            "younger than maximum age",
			retransmitKtime: dropKtimeNS + uint64(maxDropToRetransmitAge) - 1,
			shouldMatch:     true,
		},
		{
			name:            "exact maximum age",
			retransmitKtime: dropKtimeNS + uint64(maxDropToRetransmitAge),
			shouldMatch:     true,
		},
		{
			name:            "older than maximum age",
			retransmitKtime: dropKtimeNS + uint64(maxDropToRetransmitAge) + 1,
		},
		{
			name:            "drop occurs after retransmit",
			retransmitKtime: dropKtimeNS - 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Unix(1, 0)
			correlator := newTestEventCorrelator(t, 1)
			drop := testDropEvent(
				t,
				dropKtimeNS,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
				0,
				packet.TCPFlagACK,
			)
			correlator.dropStore.add(&storeEntry[*dropEvent]{value: drop}, &drop.flow, now)
			retransmit, ok := retransmitEntryFromEvent(testRetransmitEvent(
				test.retransmitKtime,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
			))
			if !ok {
				t.Fatal("retransmitEntryFromEvent() = false")
			}
			got, _ := correlator.matchAndRemoveDrop(&retransmit)
			if (got != nil) != test.shouldMatch {
				t.Fatalf("matchAndRemoveDrop() = %p, should match = %t", got, test.shouldMatch)
			}
		})
	}
}

func TestCorrelatorMatchAndRemoveDropRetainsCrossNetNSCandidate(t *testing.T) {
	now := time.Unix(1, 0)
	correlator := newTestEventCorrelator(t, 1)
	drop := testDropEvent(
		t,
		1,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
		0,
		packet.TCPFlagACK,
	)
	correlator.dropStore.add(&storeEntry[*dropEvent]{value: drop}, &drop.flow, now)

	retransmitEvent := testRetransmitEvent(
		2,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	retransmitEvent.record.NetNamespaceCookie = 2
	retransmit, ok := retransmitEntryFromEvent(retransmitEvent)
	if !ok {
		t.Fatal("retransmitEntryFromEvent() = false")
	}
	got, hasCrossNetNSCandidate := correlator.matchAndRemoveDrop(&retransmit)
	if got != nil || !hasCrossNetNSCandidate {
		t.Fatalf("cross-netns match = (%p, %t), want (nil, true)", got, hasCrossNetNSCandidate)
	}

	retransmitEvent.record.NetNamespaceCookie = 1
	retransmit, ok = retransmitEntryFromEvent(retransmitEvent)
	if !ok {
		t.Fatal("retransmitEntryFromEvent() = false")
	}
	got, _ = correlator.matchAndRemoveDrop(&retransmit)
	if got != drop {
		t.Fatalf("same-netns match = %p, want %p", got, drop)
	}
}

func TestCorrelatorMatchAndRemoveDropSelectsLatestKtimeThenSequence(t *testing.T) {
	now := time.Unix(1, 0)
	correlator := newTestEventCorrelator(t, 1)
	drops := []*dropEvent{
		testDropEvent(t, 10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK),
		testDropEvent(t, 20, "10.0.0.2", "10.0.0.1", 80, 1000, 900, 900, 200, packet.TCPFlagACK),
		testDropEvent(t, 20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK),
		testDropEvent(t, 15, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK),
	}
	for _, drop := range drops {
		correlator.dropStore.add(&storeEntry[*dropEvent]{value: drop}, &drop.flow, now)
	}
	retransmit, ok := retransmitEntryFromEvent(testRetransmitEvent(
		30,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	))
	if !ok {
		t.Fatal("retransmitEntryFromEvent() = false")
	}
	for _, index := range []int{2, 1, 3, 0} {
		got, _ := correlator.matchAndRemoveDrop(&retransmit)
		if got != drops[index] {
			t.Fatalf("matchAndRemoveDrop() = %p, want drop %d (%p)", got, index, drops[index])
		}
	}
	if got, _ := correlator.matchAndRemoveDrop(&retransmit); got != nil {
		t.Fatal("matched drop was consumed twice")
	}
}

func TestCorrelatorMatchAndRemoveRetransmitSelectsOldestArrival(t *testing.T) {
	correlator := newTestEventCorrelator(t, 1)
	now := time.Unix(1, 0)
	first := testRetransmitEvent(uint64(time.Second)+20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	second := testRetransmitEvent(uint64(time.Second)+10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	crossNetNS := testRetransmitEvent(uint64(time.Second)+30, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	crossNetNS.record.NetNamespaceCookie = 2
	for index, event := range []*retransmitEvent{first, second, crossNetNS} {
		correlator.processRetransmitEvent(event, now.Add(time.Duration(index)))
	}
	drop := testDropEvent(t, uint64(time.Second), "10.0.0.2", "10.0.0.1", 80, 1000, 900, 900, 200, packet.TCPFlagACK)
	results := correlator.processDropEvent(drop, now.Add(time.Millisecond))
	if len(results) != 1 || results[0].retransmit != first ||
		results[0].reason != types.CorrelationMatched || results[0].drop != drop {
		t.Fatalf("reverse ACK match = %v, want oldest arrival", results)
	}
	results = correlator.expireRetransmitPendingEvents(now.Add(retransmitRetentionDuration + time.Millisecond))
	if len(results) != 2 || results[0].retransmit != second || results[1].retransmit != crossNetNS {
		t.Fatalf("unmatched results = %v, want remaining retransmissions", results)
	}
	if results[0].hasCrossNetNSCandidate || !results[1].hasCrossNetNSCandidate {
		t.Fatal("matching stopped before recording cross-namespace evidence for later waiters")
	}
	if len(correlator.drainRetransmits(now.Add(retransmitRetentionDuration+time.Millisecond))) != 0 {
		t.Fatal("matched or expired retransmission was finalized twice")
	}
}

func TestCorrelatorMatchAndRemoveDropHandlesSequenceWrap(t *testing.T) {
	now := time.Unix(1, 0)
	correlator := newTestEventCorrelator(t, 1)
	drop := testDropEvent(
		t,
		1,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		math.MaxUint32-20,
		19,
		0,
		packet.TCPFlagACK,
	)
	correlator.dropStore.add(&storeEntry[*dropEvent]{value: drop}, &drop.flow, now)
	retransmit, ok := retransmitEntryFromEvent(testRetransmitEvent(
		2,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		math.MaxUint32-10,
		20,
	))
	if !ok {
		t.Fatal("retransmitEntryFromEvent() = false")
	}
	got, _ := correlator.matchAndRemoveDrop(&retransmit)
	if got != drop {
		t.Fatalf("matchAndRemoveDrop() = %p, want %p", got, drop)
	}
}
