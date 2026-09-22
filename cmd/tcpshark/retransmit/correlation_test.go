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
	"net/netip"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestNewEventCorrelator(t *testing.T) {
	before, err := timeutil.MonotonicNowNS()
	if err != nil {
		t.Fatal(err)
	}
	correlator, err := newEventCorrelator()
	if err != nil {
		t.Fatal(err)
	}
	after, err := timeutil.MonotonicNowNS()
	if err != nil {
		t.Fatal(err)
	}
	if got := correlator.readyFromMonotonicNS; got < before || got > after {
		t.Fatalf("ready ktime = %d, want within [%d, %d]", got, before, after)
	}
}

func BenchmarkCorrelateRetransmit(b *testing.B) {
	for _, dropFirst := range []bool{true, false} {
		name := "retransmit first"
		if dropFirst {
			name = "drop first"
		}
		b.Run(name, func(b *testing.B) {
			correlator, err := newEventCorrelator()
			if err != nil {
				b.Fatal(err)
			}
			correlator.readyFromMonotonicNS = 1
			event := testRetransmitEvent(uint64(time.Second)+1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
			entry, ok := retransmitEntryFromEvent(event)
			if !ok {
				b.Fatal("invalid retransmit fixture")
			}
			drop := &dropEvent{
				flow: entry.flow, namespace: entry.namespace, kernelObservedNS: uint64(time.Second),
				sequence: 100, endSequence: 200, tcpFlags: packet.TCPFlagACK,
			}
			now := time.Unix(10, 0)
			b.ReportAllocs()
			for b.Loop() {
				var results []correlationResult
				if dropFirst {
					correlator.processDropEvent(drop, now)
					results = correlator.processRetransmitEvent(event, now)
				} else {
					correlator.processRetransmitEvent(event, now)
					results = correlator.processDropEvent(drop, now)
				}
				if len(results) != 1 || results[0].drop != drop {
					b.Fatalf("correlation = %v", results)
				}
			}
		})
	}
}

func TestEventCorrelatorMatchesEitherArrivalOrder(t *testing.T) {
	tests := []struct {
		name             string
		dropArrivesFirst bool
	}{
		{name: "drop arrives first", dropArrivesFirst: true},
		{name: "retransmit arrives first"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			correlator := newTestEventCorrelator(t, 1)
			now := time.Unix(10, 0)
			drop := testDropEvent(
				t,
				uint64(time.Second),
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
				0,
				packet.TCPFlagACK,
			)
			retransmit := testRetransmitEvent(
				uint64(time.Second)+1,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
			)

			var results []correlationResult
			if test.dropArrivesFirst {
				got := correlator.processDropEvent(drop, now)
				if len(got) != 0 {
					t.Fatalf("processDrop() = %v, want no result", got)
				}
				results = correlator.processRetransmitEvent(
					retransmit,
					now.Add(time.Millisecond),
				)
			} else {
				got := correlator.processRetransmitEvent(
					retransmit,
					now,
				)
				if len(got) != 0 {
					t.Fatalf("processRetransmit() = %v, want pending", got)
				}
				results = correlator.processDropEvent(
					drop,
					now.Add(retransmitRetentionDuration-time.Nanosecond),
				)
			}

			if len(results) != 1 || results[0].reason != types.CorrelationMatched ||
				results[0].drop != drop || results[0].retransmit != retransmit || !results[0].netNamespace {
				t.Fatalf("result = %+v, want one matched retransmission", results)
			}
			if correlator.retransmitStore.byDeadline.Len() != 0 {
				t.Fatalf("waiting retransmits = %d, want 0", correlator.retransmitStore.byDeadline.Len())
			}
		})
	}
}

func TestCorrelatorEvictOldestArrival(t *testing.T) {
	now := time.Unix(1, 0)
	correlator := newTestEventCorrelator(t, 1)
	correlator.dropStore.capacity = 2
	drops := []*dropEvent{
		testDropEvent(t, 30, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK),
		testDropEvent(t, 10, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 400, 0, packet.TCPFlagACK),
		testDropEvent(t, 20, "10.0.0.1", "10.0.0.2", 1000, 80, 500, 600, 0, packet.TCPFlagACK),
	}
	for dropIndex, drop := range drops {
		correlator.dropStore.add(&storeEntry[*dropEvent]{value: drop}, &drop.flow, now.Add(time.Duration(dropIndex)))
	}
	if correlator.dropStore.byDeadline.Len() != 2 {
		t.Fatalf("byDeadline.Len() = %d, want 2", correlator.dropStore.byDeadline.Len())
	}
	oldest := correlator.dropStore.byDeadline.Front().Value.(*storeEntry[*dropEvent])
	if oldest.value != drops[1] {
		t.Fatalf("oldest retained drop = %p, want second arrival %p", oldest.value, drops[1])
	}
}

func TestCorrelatorExpireBySupportedRetention(t *testing.T) {
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
	correlator.expireRetransmitPendingEvents(now.Add(dropRetentionDuration - time.Nanosecond))
	if correlator.dropStore.byDeadline.Len() != 1 {
		t.Fatalf("drop expired before retention deadline")
	}
	correlator.expireRetransmitPendingEvents(now.Add(dropRetentionDuration))
	if correlator.dropStore.byDeadline.Len() != 0 || len(correlator.dropStore.byFlow) != 0 {
		t.Fatalf(
			"expired state = age %d flows %d, want empty",
			correlator.dropStore.byDeadline.Len(),
			len(correlator.dropStore.byFlow),
		)
	}
}

func TestCorrelatorExpiresDropsBeforeProcessingInput(t *testing.T) {
	for _, input := range []string{"retransmit", "drop"} {
		t.Run(input, func(t *testing.T) {
			correlator := newTestEventCorrelator(t, 1)
			now := time.Unix(1, 0)
			old := testDropEvent(t, 1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK)
			correlator.processDropEvent(old, now)
			later := now.Add(dropRetentionDuration)
			if input == "retransmit" {
				event := testRetransmitEvent(2, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
				results := correlator.processRetransmitEvent(event, later)
				if len(results) != 0 || correlator.dropStore.byDeadline.Len() != 0 {
					t.Fatalf("expired candidate matched: results=%v", results)
				}
				if correlator.retransmitStore.byDeadline.Len() != 1 {
					t.Fatal("unmatched retransmit was not queued")
				}
			} else {
				next := testDropEvent(t, 2, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK)
				correlator.processDropEvent(next, later)
				if correlator.dropStore.byDeadline.Len() != 1 || len(correlator.dropStore.entriesForFlow(next.flow)) != 1 ||
					correlator.dropStore.byDeadline.Front().Value.(*storeEntry[*dropEvent]).value != next {
					t.Fatal("expired candidate remained after storing a new drop")
				}
			}
		})
	}
}

func TestEventCorrelatorWaitDeadline(t *testing.T) {
	tests := []struct {
		name        string
		elapsed     time.Duration
		wantResults int
	}{
		{name: "before deadline", elapsed: retransmitRetentionDuration - time.Nanosecond},
		{name: "at deadline", elapsed: retransmitRetentionDuration, wantResults: 1},
		{name: "after deadline", elapsed: retransmitRetentionDuration + time.Nanosecond, wantResults: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const readyKtimeNS = uint64(1)
			correlator := newTestEventCorrelator(t, readyKtimeNS)
			now := time.Unix(20, 0)
			event := testRetransmitEvent(
				readyKtimeNS+uint64(maxDropToRetransmitAge),
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
			)
			results := correlator.processRetransmitEvent(
				event,
				now,
			)
			if len(results) != 0 {
				t.Fatalf("processRetransmit() = %v, want pending", results)
			}

			results = correlator.expireRetransmitPendingEvents(now.Add(test.elapsed))
			if len(results) != test.wantResults {
				t.Fatalf("expired results = %d, want %d", len(results), test.wantResults)
			}
			if test.wantResults == 1 && results[0].reason != types.CorrelationWaitTimeout {
				t.Fatalf("reason = %v, want wait_timeout", results[0].reason)
			}
		})
	}
}

func TestEventCorrelatorUnmatchedDiagnostics(t *testing.T) {
	const readyKtimeNS = uint64(100)
	tests := []struct {
		name                  string
		kernelObservedNS      uint64
		prepare               func(*eventCorrelator, time.Time)
		wantIncompleteHistory bool
		wantMatchedNamespace  bool
	}{
		{name: "before startup", kernelObservedNS: readyKtimeNS - 1, wantIncompleteHistory: true},
		{name: "before history horizon", kernelObservedNS: readyKtimeNS + uint64(maxDropToRetransmitAge) - 1, wantIncompleteHistory: true},
		{name: "at history horizon", kernelObservedNS: readyKtimeNS + uint64(maxDropToRetransmitAge)},
		{
			name:             "unusable drop does not change outcome",
			kernelObservedNS: readyKtimeNS + uint64(maxDropToRetransmitAge),
			prepare: func(c *eventCorrelator, now time.Time) {
				c.processDropEvent(&dropEvent{kernelObservedNS: readyKtimeNS}, now)
			},
		},
		{
			name:                 "drop cache eviction does not change outcome",
			kernelObservedNS:     readyKtimeNS + uint64(maxDropToRetransmitAge),
			wantMatchedNamespace: true,
			prepare: func(c *eventCorrelator, now time.Time) {
				c.dropStore.capacity = 1
				for sequence := range uint32(2) {
					c.processDropEvent(&dropEvent{
						kernelObservedNS: readyKtimeNS + uint64(sequence),
						namespace:        namespaceID{cookie: 1},
						flow:             testFlowKey(1000, 80),
						sequence:         sequence, endSequence: sequence + 1,
					}, now)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			correlator := newTestEventCorrelator(t, readyKtimeNS)
			now := time.Unix(1, 0)
			if test.prepare != nil {
				test.prepare(correlator, now)
			}
			event := testRetransmitEvent(test.kernelObservedNS, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
			if results := correlator.processRetransmitEvent(event, now); len(results) != 0 {
				t.Fatalf("results = %+v, want pending event", results)
			}
			results := correlator.expireRetransmitPendingEvents(now.Add(retransmitRetentionDuration))
			if len(results) != 1 || results[0].reason != types.CorrelationWaitTimeout || results[0].drop != nil {
				t.Fatalf("results = %+v, want wait_timeout", results)
			}
			if results[0].isStartupHistoryIncomplete != test.wantIncompleteHistory ||
				results[0].netNamespace != test.wantMatchedNamespace {
				t.Fatalf("diagnostics = %+v, want incomplete history=%t and matched namespace=%t",
					results[0], test.wantIncompleteHistory, test.wantMatchedNamespace)
			}
		})
	}
}

func TestEventCorrelatorRejectsUnsupportedRetransmit(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*abi.TCPRetransmitEvent)
	}{
		{name: "missing timestamp", mutate: func(e *abi.TCPRetransmitEvent) { e.KernelObservedNS = 0 }},
		{name: "missing namespace", mutate: func(e *abi.TCPRetransmitEvent) { e.NetNamespaceCookie = 0; e.NetNamespaceInum = 0 }},
		{name: "unsupported family", mutate: func(e *abi.TCPRetransmitEvent) { e.Family = 0 }},
		{name: "empty sequence range", mutate: func(e *abi.TCPRetransmitEvent) { e.TCPEndSeq = e.TCPSeq }},
		{name: "reset segment", mutate: func(e *abi.TCPRetransmitEvent) { e.TCPFlags = packet.TCPFlagRST }},
	} {
		t.Run(test.name, func(t *testing.T) {
			correlator := newTestEventCorrelator(t, 1)
			event := testRetransmitEvent(2, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
			test.mutate(&event.record)
			now := time.Unix(1, 0)
			results := correlator.processRetransmitEvent(event, now)
			if len(results) != 1 || results[0].retransmit != event ||
				results[0].reason != types.CorrelationUnsupported || results[0].drop != nil ||
				!results[0].isStartupHistoryIncomplete {
				t.Fatalf("results = %+v, want unsupported with incomplete startup history", results)
			}
			if remaining := correlator.drainRetransmits(now); len(remaining) != 0 {
				t.Fatalf("unsupported retransmit was also queued: %+v", remaining)
			}
		})
	}
}

func TestEventCorrelatorDrainDeadline(t *testing.T) {
	for _, test := range []struct {
		name    string
		elapsed time.Duration
		want    types.CorrelationReason
	}{
		{name: "before deadline", elapsed: retransmitRetentionDuration - time.Nanosecond, want: types.CorrelationInterrupted},
		{name: "at deadline", elapsed: retransmitRetentionDuration, want: types.CorrelationWaitTimeout},
		{name: "after deadline", elapsed: retransmitRetentionDuration + time.Nanosecond, want: types.CorrelationWaitTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			correlator := newTestEventCorrelator(t, 1)
			now := time.Unix(1, 0)
			event := testRetransmitEvent(2, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
			correlator.processRetransmitEvent(event, now)
			results := correlator.drainRetransmits(now.Add(test.elapsed))
			if len(results) != 1 || results[0].reason != test.want || results[0].drop != nil {
				t.Fatalf("drained = %+v, want %q", results, test.want)
			}
			if remaining := correlator.drainRetransmits(now.Add(time.Second)); len(remaining) != 0 {
				t.Fatalf("drained twice: %+v", remaining)
			}
		})
	}
}

func TestEventCorrelatorDrainRetransmits(t *testing.T) {
	correlator := newTestEventCorrelator(t, 1)
	correlator.retransmitStore.capacity = 2
	now := time.Unix(30, 0)
	events := []*retransmitEvent{
		testRetransmitEvent(uint64(time.Second)+1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200),
		testRetransmitEvent(uint64(time.Second)+2, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 400),
		testRetransmitEvent(uint64(time.Second)+3, "10.0.0.1", "10.0.0.2", 1000, 80, 500, 600),
	}

	var emitted []correlationResult
	for eventIndex, event := range events {
		results := correlator.processRetransmitEvent(
			event,
			now.Add(time.Duration(eventIndex)),
		)
		emitted = append(emitted, results...)
	}
	if len(emitted) != 1 || emitted[0].retransmit != events[0] ||
		emitted[0].reason != types.CorrelationQueueFull {
		t.Fatalf("capacity results = %+v, want first event with capacity reason", emitted)
	}

	stopTime := now.Add(retransmitRetentionDuration + time.Nanosecond)
	settled := correlator.drainRetransmits(stopTime)
	if len(settled) != 2 ||
		settled[0].retransmit != events[1] || settled[1].retransmit != events[2] {
		t.Fatalf("settled events = %+v, want remaining events in deadline order", settled)
	}
	wantReasons := []types.CorrelationReason{types.CorrelationWaitTimeout, types.CorrelationInterrupted}
	for resultIndex, result := range settled {
		if result.reason != wantReasons[resultIndex] || result.drop != nil {
			t.Fatalf("drained result %d = %+v, want %q", resultIndex, result, wantReasons[resultIndex])
		}
	}
	if again := correlator.drainRetransmits(stopTime); len(again) != 0 {
		t.Fatalf("second drain returned %d events, want 0", len(again))
	}
	if correlator.retransmitStore.byDeadline.Len() != 0 ||
		len(correlator.retransmitStore.byFlow) != 0 {
		t.Fatalf(
			"waiting state = deadlines %d flows %d, want empty",
			correlator.retransmitStore.byDeadline.Len(),
			len(correlator.retransmitStore.byFlow),
		)
	}
}

func TestEventCorrelatorNetNamespace(t *testing.T) {
	const retransmitKtime = uint64(2 * time.Second)
	newDrop := func(cookie uint64) *dropEvent {
		drop := testDropEvent(t, retransmitKtime-1, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 400, 0, packet.TCPFlagACK)
		drop.namespace.cookie = cookie
		return drop
	}
	otherFlow := newDrop(1)
	otherFlow.flow = testFlowKey(1001, 80)
	otherNamespace := newDrop(2)
	otherNamespace.sequence, otherNamespace.endSequence = 100, 200
	unknownNamespace := newDrop(0)
	unknownNamespace.namespace = namespaceID{}
	oldDrop := newDrop(1)
	oldDrop.kernelObservedNS = retransmitKtime - uint64(maxDropToRetransmitAge) - 1
	futureDrop := newDrop(1)
	futureDrop.kernelObservedNS = retransmitKtime + 1
	reverseACK := testDropEvent(t, retransmitKtime-1, "10.0.0.2", "10.0.0.1", 80, 1000, 900, 900, 100, packet.TCPFlagACK)

	for _, dropFirst := range []bool{true, false} {
		order := "retransmit first"
		if dropFirst {
			order = "drop first"
		}
		t.Run(order, func(t *testing.T) {
			for _, test := range []struct {
				name  string
				drops []*dropEvent
				want  bool
			}{
				{name: "no candidate"},
				{name: "different flow", drops: []*dropEvent{otherFlow}},
				{name: "different namespace", drops: []*dropEvent{otherNamespace}},
				{name: "unknown namespace", drops: []*dropEvent{unknownNamespace}},
				{name: "inode fallback", drops: []*dropEvent{newDrop(0)}, want: true},
				{name: "sequence mismatch", drops: []*dropEvent{newDrop(1)}, want: true},
				{name: "drop too old", drops: []*dropEvent{oldDrop}, want: true},
				{name: "future drop", drops: []*dropEvent{futureDrop}, want: true},
				{name: "reverse ACK does not cover range", drops: []*dropEvent{reverseACK}, want: true},
				{name: "same then different namespace", drops: []*dropEvent{newDrop(1), otherNamespace}, want: true},
				{name: "different then same namespace", drops: []*dropEvent{otherNamespace, newDrop(1)}, want: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					correlator := newTestEventCorrelator(t, 1)
					now := time.Unix(40, 0)
					retransmit := testRetransmitEvent(retransmitKtime, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
					var results []correlationResult
					if !dropFirst {
						results = append(results, correlator.processRetransmitEvent(retransmit, now)...)
					}
					for _, drop := range test.drops {
						results = append(results, correlator.processDropEvent(drop, now)...)
					}
					if dropFirst {
						results = append(results, correlator.processRetransmitEvent(retransmit, now)...)
					}
					if len(results) != 0 {
						t.Fatalf("namespace check finalized retransmit: %+v", results)
					}
					results = correlator.expireRetransmitPendingEvents(now.Add(retransmitRetentionDuration))
					if len(results) != 1 || results[0].reason != types.CorrelationWaitTimeout ||
						results[0].drop != nil || results[0].netNamespace != test.want {
						t.Fatalf("expired results = %+v, want wait_timeout and matched namespace=%t", results, test.want)
					}
				})
			}
		})
	}
}

func TestEventCorrelatorFinalizesWithConcurrentDiagnostics(t *testing.T) {
	for _, reason := range []types.CorrelationReason{
		types.CorrelationWaitTimeout, types.CorrelationQueueFull, types.CorrelationInterrupted,
	} {
		t.Run(string(reason), func(t *testing.T) {
			const ready = uint64(2 * time.Second)
			correlator := newTestEventCorrelator(t, ready)
			correlator.retransmitStore.capacity = 1
			now := time.Unix(1, 0)
			event := testRetransmitEvent(ready+1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
			correlator.processRetransmitEvent(event, now)
			drop := testDropEvent(t, ready, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 400, 0, packet.TCPFlagACK)
			correlator.processDropEvent(drop, now)
			var results []correlationResult
			switch reason {
			case types.CorrelationWaitTimeout:
				results = correlator.expireRetransmitPendingEvents(now.Add(retransmitRetentionDuration))
			case types.CorrelationQueueFull:
				next := testRetransmitEvent(ready+2, "10.0.0.1", "10.0.0.2", 1001, 80, 100, 200)
				results = correlator.processRetransmitEvent(next, now)
			case types.CorrelationInterrupted:
				results = correlator.drainRetransmits(now)
			}
			if len(results) != 1 || results[0].retransmit != event || results[0].reason != reason || results[0].drop != nil {
				t.Fatalf("results = %+v, want original event finalized as %q", results, reason)
			}
			if !results[0].isStartupHistoryIncomplete || !results[0].netNamespace {
				t.Fatalf("result = %+v, want both diagnostics retained", results[0])
			}
		})
	}
}

func TestEventCorrelatorSettlesBeforeCurrentDrop(t *testing.T) {
	tests := []struct {
		name      string
		elapsed   time.Duration
		wantMatch bool
	}{
		{name: "before deadline", elapsed: retransmitRetentionDuration - time.Nanosecond, wantMatch: true},
		{name: "at deadline", elapsed: retransmitRetentionDuration},
		{name: "after deadline", elapsed: retransmitRetentionDuration + time.Nanosecond},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			correlator := newTestEventCorrelator(t, 1)
			now := time.Unix(50, 0)
			retransmit := testRetransmitEvent(
				uint64(time.Second)+1,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
			)
			if results := correlator.processRetransmitEvent(retransmit, now); len(results) != 0 {
				t.Fatalf("processRetransmit() = %v, want pending", results)
			}
			drop := testDropEvent(
				t,
				uint64(time.Second),
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
				0,
				packet.TCPFlagACK,
			)
			results := correlator.processDropEvent(drop, now.Add(test.elapsed))
			if len(results) != 1 {
				t.Fatalf("processDrop() = %v, want one result", results)
			}
			if test.wantMatch && (results[0].reason != types.CorrelationMatched || results[0].drop != drop) {
				t.Fatalf("result = %+v, want match", results[0])
			}
			if !test.wantMatch && (results[0].drop != nil ||
				results[0].reason != types.CorrelationWaitTimeout) {
				t.Fatalf("result = %+v, want expired no-match", results[0])
			}
			if !test.wantMatch && len(correlator.drainRetransmits(now)) != 0 {
				t.Fatal("shutdown returned the expired retransmission again")
			}
		})
	}
}

func TestEventCorrelatorSettlesBeforeCapacityCheck(t *testing.T) {
	correlator := newTestEventCorrelator(t, 1)
	correlator.retransmitStore.capacity = 1
	now := time.Unix(60, 0)
	first := testRetransmitEvent(
		uint64(time.Second)+1,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	second := testRetransmitEvent(
		uint64(time.Second)+2,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		300,
		400,
	)
	if results := correlator.processRetransmitEvent(first, now); len(results) != 0 {
		t.Fatalf("first processRetransmit() = %v, want pending", results)
	}
	results := correlator.processRetransmitEvent(
		second,
		now.Add(retransmitRetentionDuration),
	)
	if len(results) != 1 || results[0].retransmit != first {
		t.Fatalf("second processRetransmit() = %v, want expired first", results)
	}
	if results[0].reason != types.CorrelationWaitTimeout {
		t.Fatalf("reason = %v, want wait_timeout", results[0].reason)
	}
	if correlator.retransmitStore.byDeadline.Len() != 1 {
		t.Fatalf("waiting retransmits = %d, want second only", correlator.retransmitStore.byDeadline.Len())
	}
}

func TestCorrelatorNextDeadlineIncludesBothStores(t *testing.T) {
	for _, test := range []struct {
		name         string
		waitingDelay time.Duration
	}{
		{name: "waiting expires first"},
		{name: "drop expires first", waitingDelay: dropRetentionDuration - retransmitRetentionDuration/2},
	} {
		t.Run(test.name, func(t *testing.T) {
			correlator := newTestEventCorrelator(t, 1)
			if _, ok := correlator.nextDeadline(); ok {
				t.Fatal("empty correlator has a deadline")
			}
			now := time.Unix(1, 0)
			drop := testDropEvent(t, 1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK)
			correlator.processDropEvent(drop, now)
			dropDeadline := now.Add(dropRetentionDuration)
			if got, ok := correlator.nextDeadline(); !ok || got != dropDeadline {
				t.Fatalf("drop-only deadline = (%s, %t), want %s", got, ok, dropDeadline)
			}
			waitingAt := now.Add(test.waitingDelay)
			event := testRetransmitEvent(2, "10.0.0.1", "10.0.0.2", 2000, 80, 100, 200)
			correlator.processRetransmitEvent(event, waitingAt)
			waitingDeadline := waitingAt.Add(retransmitRetentionDuration)
			first, second := waitingDeadline, dropDeadline
			if dropDeadline.Before(waitingDeadline) {
				first, second = dropDeadline, waitingDeadline
			}
			if got, ok := correlator.nextDeadline(); !ok || got != first {
				t.Fatalf("nextDeadline() = (%s, %t), want %s", got, ok, first)
			}
			results := correlator.expireRetransmitPendingEvents(first)
			if got, ok := correlator.nextDeadline(); !ok || got != second {
				t.Fatalf("deadline after expiration = (%s, %t), want %s", got, ok, second)
			}
			results = append(results, correlator.expireRetransmitPendingEvents(second)...)
			if len(results) != 1 || results[0].retransmit != event || results[0].drop != nil {
				t.Fatalf("expiration results = %v, want one unmatched retransmission", results)
			}
			if _, ok := correlator.nextDeadline(); ok || len(correlator.dropStore.byFlow) != 0 {
				t.Fatal("expired entries retain a deadline or drop flow")
			}
		})
	}
}

func newTestEventCorrelator(
	t *testing.T,
	readyFromMonotonicNS uint64,
) *eventCorrelator {
	t.Helper()
	correlator, err := newEventCorrelator()
	if err != nil {
		t.Fatalf("newEventCorrelator() error = %v", err)
	}
	correlator.readyFromMonotonicNS = readyFromMonotonicNS
	return correlator
}

func testFlowKey(sourcePort, destinationPort uint16) flowKey {
	sourceAddress := netip.MustParseAddr("10.0.0.1")
	destinationAddress := netip.MustParseAddr("10.0.0.2")
	return flowKey{
		source:      netip.AddrPortFrom(sourceAddress, sourcePort),
		destination: netip.AddrPortFrom(destinationAddress, destinationPort),
	}
}
