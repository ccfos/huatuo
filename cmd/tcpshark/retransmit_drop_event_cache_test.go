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

package main

import (
	"math"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/packet"
)

func TestDropwatchCandidatesEvictOldestArrival(t *testing.T) {
	now := time.Unix(1, 0)
	candidates := newDropwatchCandidates(2)
	drops := []*dropEvent{
		testDropEvent(t, 30, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK),
		testDropEvent(t, 10, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 400, 0, packet.TCPFlagACK),
		testDropEvent(t, 20, "10.0.0.1", "10.0.0.2", 1000, 80, 500, 600, 0, packet.TCPFlagACK),
	}
	for dropIndex, drop := range drops {
		candidates.storeDrop(drop, now.Add(time.Duration(dropIndex)))
	}
	if candidates.byAge.Len() != 2 {
		t.Fatalf("byAge.Len() = %d, want 2", candidates.byAge.Len())
	}
	oldest := candidates.byAge.Front().Value.(*dropCandidate)
	if oldest.event != drops[1] {
		t.Fatalf("oldest retained drop = %p, want second arrival %p", oldest.event, drops[1])
	}
}

func TestDropwatchCandidatesCapacityBoundary(t *testing.T) {
	now := time.Unix(1, 0)
	candidates := newDropwatchCandidates(dropwatchCandidateCapacity)
	flow := testFlowKey(1000, 80)
	for dropIndex := 0; dropIndex <= dropwatchCandidateCapacity; dropIndex++ {
		event := &dropEvent{
			flow:      flow,
			ktimeNS:   uint64(dropIndex + 1),
			namespace: namespaceID{cookie: 1},
		}
		candidates.storeDrop(event, now)
	}
	if candidates.byAge.Len() != dropwatchCandidateCapacity ||
		len(candidates.byFlow[flow]) != dropwatchCandidateCapacity {
		t.Fatalf(
			"capacity state = age %d flow %d, want %d",
			candidates.byAge.Len(),
			len(candidates.byFlow[flow]),
			dropwatchCandidateCapacity,
		)
	}
	oldest := candidates.byAge.Front().Value.(*dropCandidate)
	if oldest.event.ktimeNS != 2 {
		t.Fatalf("oldest retained ktime = %d, want 2", oldest.event.ktimeNS)
	}
}

func TestDropwatchCandidatesExpireBySupportedRetention(t *testing.T) {
	now := time.Unix(1, 0)
	candidates := newDropwatchCandidates(dropwatchCandidateCapacity)
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
	candidates.storeDrop(drop, now)
	candidates.discardExpiredDrops(now.Add(dropRetentionDuration))
	if candidates.byAge.Len() != 1 {
		t.Fatalf("drop expired at inclusive retention boundary")
	}
	candidates.discardExpiredDrops(now.Add(dropRetentionDuration + time.Nanosecond))
	if candidates.byAge.Len() != 0 || len(candidates.byFlow) != 0 {
		t.Fatalf(
			"expired state = age %d flows %d, want empty",
			candidates.byAge.Len(),
			len(candidates.byFlow),
		)
	}
}

func TestDropwatchCandidatesTakeMatchingDropBothFlowDirections(t *testing.T) {
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
			candidates := newDropwatchCandidates(dropwatchCandidateCapacity)
			candidates.storeDrop(test.drop, now)
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
			got, hasCrossNetNSCandidate := candidates.takeMatchingDrop(&match, now)
			if got != test.drop || hasCrossNetNSCandidate {
				t.Fatalf(
					"takeMatchingDrop() = (%p, %t), want (%p, false)",
					got,
					hasCrossNetNSCandidate,
					test.drop,
				)
			}
		})
	}
}

func TestDropwatchCandidatesTakeMatchingDropEnforcesCausalAge(t *testing.T) {
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
			candidates := newDropwatchCandidates(dropwatchCandidateCapacity)
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
			candidates.storeDrop(drop, now)
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
			got, _ := candidates.takeMatchingDrop(&retransmit, now)
			if (got != nil) != test.shouldMatch {
				t.Fatalf("takeMatchingDrop() = %p, should match = %t", got, test.shouldMatch)
			}
		})
	}
}

func TestDropwatchCandidatesTakeMatchingDropRetainsCrossNetNSCandidate(t *testing.T) {
	now := time.Unix(1, 0)
	candidates := newDropwatchCandidates(dropwatchCandidateCapacity)
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
	candidates.storeDrop(drop, now)

	retransmitEvent := testRetransmitEvent(
		2,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	retransmitEvent.NetNamespaceCookie = 2
	retransmit, ok := retransmitEntryFromEvent(retransmitEvent)
	if !ok {
		t.Fatal("retransmitEntryFromEvent() = false")
	}
	got, hasCrossNetNSCandidate := candidates.takeMatchingDrop(&retransmit, now)
	if got != nil || !hasCrossNetNSCandidate {
		t.Fatalf("cross-netns match = (%p, %t), want (nil, true)", got, hasCrossNetNSCandidate)
	}

	retransmitEvent.NetNamespaceCookie = 1
	retransmit, ok = retransmitEntryFromEvent(retransmitEvent)
	if !ok {
		t.Fatal("retransmitEntryFromEvent() = false")
	}
	got, _ = candidates.takeMatchingDrop(&retransmit, now)
	if got != drop {
		t.Fatalf("same-netns match = %p, want %p", got, drop)
	}
}

func TestDropwatchCandidatesTakeMatchingDropSelectsLatestKtimeThenID(t *testing.T) {
	now := time.Unix(1, 0)
	candidates := newDropwatchCandidates(dropwatchCandidateCapacity)
	drops := []*dropEvent{
		testDropEvent(t, 10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK),
		testDropEvent(t, 20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK),
		testDropEvent(t, 20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK),
	}
	for _, drop := range drops {
		candidates.storeDrop(drop, now)
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
	got, _ := candidates.takeMatchingDrop(&retransmit, now)
	if got != drops[2] {
		t.Fatalf("takeMatchingDrop() = %p, want latest ID %p", got, drops[2])
	}
	if candidates.byAge.Len() != 2 || len(candidates.byFlow[retransmit.flow]) != 2 {
		t.Fatalf(
			"post-match state = age %d flow %d, want 2",
			candidates.byAge.Len(),
			len(candidates.byFlow[retransmit.flow]),
		)
	}
}

func TestDropwatchCandidatesTakeMatchingDropHandlesSequenceWrap(t *testing.T) {
	now := time.Unix(1, 0)
	candidates := newDropwatchCandidates(dropwatchCandidateCapacity)
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
	candidates.storeDrop(drop, now)
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
	got, _ := candidates.takeMatchingDrop(&retransmit, now)
	if got != drop {
		t.Fatalf("takeMatchingDrop() = %p, want %p", got, drop)
	}
}

func TestRetransmitWaitQueueCapacityBoundary(t *testing.T) {
	queue := newRetransmitWaitQueue(retransmitWaitCapacity)
	flow := testFlowKey(1000, 80)
	var first *waitingRetransmit
	for retransmitIndex := 0; retransmitIndex <= retransmitWaitCapacity; retransmitIndex++ {
		waiting := &waitingRetransmit{
			match:    retransmitEntry{flow: flow},
			deadline: time.Unix(1, int64(retransmitIndex)),
			id:       uint64(retransmitIndex + 1),
		}
		if retransmitIndex == 0 {
			first = waiting
		}
		evicted := queue.addRetransmit(waiting)
		switch {
		case retransmitIndex < retransmitWaitCapacity && evicted != nil:
			t.Fatalf("addRetransmit(%d) evicted before capacity", retransmitIndex)
		case retransmitIndex == retransmitWaitCapacity && evicted != first:
			t.Fatalf("capacity eviction = %p, want first retransmit %p", evicted, first)
		}
	}
	if queue.byDeadline.Len() != retransmitWaitCapacity ||
		len(queue.byFlow[flow]) != retransmitWaitCapacity {
		t.Fatalf(
			"capacity state = deadlines %d flow %d, want %d",
			queue.byDeadline.Len(),
			len(queue.byFlow[flow]),
			retransmitWaitCapacity,
		)
	}
}

func TestRetransmitWaitQueueNextDeadline(t *testing.T) {
	queue := newRetransmitWaitQueue(1)
	if _, ok := queue.nextDeadline(); ok {
		t.Fatal("nextDeadline() = true for empty queue")
	}

	want := time.Unix(1, 0)
	queue.addRetransmit(&waitingRetransmit{deadline: want})
	if deadline, ok := queue.nextDeadline(); !ok || deadline != want {
		t.Fatalf("nextDeadline() = (%s, %t), want (%s, true)", deadline, ok, want)
	}
}

func TestRetransmitWaitQueueTakesOneRecordAtATime(t *testing.T) {
	queue := newRetransmitWaitQueue(2)
	firstDeadline := time.Unix(2, 0)
	first := &waitingRetransmit{
		match:    retransmitEntry{flow: testFlowKey(1000, 80)},
		deadline: firstDeadline,
	}
	second := &waitingRetransmit{
		match:    retransmitEntry{flow: testFlowKey(2000, 80)},
		deadline: firstDeadline.Add(time.Second),
	}
	queue.addRetransmit(first)
	queue.addRetransmit(second)

	if got := queue.takeNextExpiredRetransmit(firstDeadline.Add(-time.Nanosecond)); got != nil {
		t.Fatalf("takeNextExpiredRetransmit() = %p before deadline, want nil", got)
	}
	if got := queue.takeNextExpiredRetransmit(firstDeadline); got != first {
		t.Fatalf("takeNextExpiredRetransmit() = %p, want first %p", got, first)
	}
	if got := queue.takeNextRetransmit(); got != second {
		t.Fatalf("takeNextRetransmit() = %p, want second %p", got, second)
	}
	if queue.byDeadline.Len() != 0 || len(queue.byFlow) != 0 {
		t.Fatalf(
			"queue state = deadlines %d flows %d, want empty",
			queue.byDeadline.Len(),
			len(queue.byFlow),
		)
	}
}
