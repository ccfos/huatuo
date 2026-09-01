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
	"slices"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestRetransmitDropCorrelatorMatchesEitherArrivalOrder(t *testing.T) {
	tests := []struct {
		name             string
		dropArrivesFirst bool
	}{
		{name: "drop arrives first", dropArrivesFirst: true},
		{name: "retransmit arrives first"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			correlator := newTestRetransmitDropCorrelator(t, 1)
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

			var results []retransmitDropResult
			if test.dropArrivesFirst {
				got, err := correlator.processDrop(drop, now)
				if err != nil || len(got) != 0 {
					t.Fatalf("processDrop() = (%v, %v), want no result", got, err)
				}
				results, err = correlator.processRetransmit(
					retransmit,
					now.Add(time.Millisecond),
				)
				if err != nil {
					t.Fatalf("processRetransmit() error = %v", err)
				}
			} else {
				got, err := correlator.processRetransmit(
					retransmit,
					now,
				)
				if err != nil || len(got) != 0 {
					t.Fatalf("processRetransmit() = (%v, %v), want pending", got, err)
				}
				results, err = correlator.processDrop(
					drop,
					now.Add(retransmitDropWaitDuration-time.Nanosecond),
				)
				if err != nil {
					t.Fatalf("processDrop() error = %v", err)
				}
			}

			if len(results) != 1 || results[0].drop != drop ||
				results[0].retransmit != retransmit {
				t.Fatalf("result = %+v, want one matched retransmission", results)
			}
			if retransmit.DropLocation != "host_software" {
				t.Fatalf("drop location = %q, want host_software", retransmit.DropLocation)
			}
			if correlator.waitingRetransmits.byDeadline.Len() != 0 {
				t.Fatalf("waiting retransmits = %d, want 0", correlator.waitingRetransmits.byDeadline.Len())
			}
		})
	}
}

func TestRetransmitDropCorrelatorWaitDeadline(t *testing.T) {
	tests := []struct {
		name        string
		elapsed     time.Duration
		wantResults int
	}{
		{name: "before deadline", elapsed: retransmitDropWaitDuration - time.Nanosecond},
		{name: "at deadline", elapsed: retransmitDropWaitDuration, wantResults: 1},
		{name: "after deadline", elapsed: retransmitDropWaitDuration + time.Nanosecond, wantResults: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const readyKtimeNS = uint64(1)
			correlator := newTestRetransmitDropCorrelator(t, readyKtimeNS)
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
			results, err := correlator.processRetransmit(
				event,
				now,
			)
			if err != nil || len(results) != 0 {
				t.Fatalf("processRetransmit() = (%v, %v), want pending", results, err)
			}

			results = correlator.settleExpiredRetransmits(now.Add(test.elapsed))
			if len(results) != test.wantResults {
				t.Fatalf("expired results = %d, want %d", len(results), test.wantResults)
			}
			if test.wantResults == 1 && !hasResultCorrelationReason(
				results[0],
				types.CorrelationReasonNoMatchingDrop,
			) {
				t.Fatalf("reasons = %v, want no_matching_drop", results[0].correlationReasons)
			}
		})
	}
}

func TestRetransmitDropCorrelatorReasons(t *testing.T) {
	const readyKtimeNS = uint64(100)
	tests := []struct {
		name       string
		ktimeNS    uint64
		prepare    func(*testing.T, *retransmitDropCorrelator)
		wantReason types.CorrelationReason
		wantAbsent types.CorrelationReason
	}{
		{
			name:       "startup history before horizon",
			ktimeNS:    readyKtimeNS + uint64(maxDropToRetransmitAge) - 1,
			wantReason: types.CorrelationReasonStartupHistoryIncomplete,
		},
		{
			name:       "startup history recovers at horizon",
			ktimeNS:    readyKtimeNS + uint64(maxDropToRetransmitAge),
			wantAbsent: types.CorrelationReasonStartupHistoryIncomplete,
		},
		{
			name:    "unusable drop has no dedicated reason",
			ktimeNS: readyKtimeNS + uint64(maxDropToRetransmitAge),
			prepare: func(t *testing.T, c *retransmitDropCorrelator) {
				_, err := c.processDrop(&dropEvent{
					ktimeNS: readyKtimeNS + uint64(maxDropToRetransmitAge),
				}, time.Unix(1, 0))
				if err != nil {
					t.Fatalf("processDrop() error = %v", err)
				}
			},
			wantAbsent: types.CorrelationReason("drop_evidence_unusable"),
		},
		{
			name:    "evicted drop has no dedicated reason",
			ktimeNS: readyKtimeNS + uint64(maxDropToRetransmitAge),
			prepare: func(t *testing.T, c *retransmitDropCorrelator) {
				c.drops.capacity = 1
				now := time.Unix(1, 0)
				for sequence := range uint32(2) {
					_, err := c.processDrop(&dropEvent{
						ktimeNS:     readyKtimeNS + uint64(sequence),
						namespace:   namespaceID{cookie: 1},
						flow:        testFlowKey(1000, 80),
						sequence:    sequence,
						endSequence: sequence + 1,
					}, now.Add(time.Duration(sequence)))
					if err != nil {
						t.Fatalf("processDrop() error = %v", err)
					}
				}
			},
			wantAbsent: types.CorrelationReason("drop_evidence_evicted"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			correlator := newTestRetransmitDropCorrelator(t, readyKtimeNS)
			if test.prepare != nil {
				test.prepare(t, correlator)
			}
			event := &types.TCPRetransmitTracing{KtimeNS: test.ktimeNS}
			result := correlator.noMatchResult(event, false)
			if test.wantReason != "" && !hasResultCorrelationReason(result, test.wantReason) {
				t.Fatalf("reasons = %v, want %q", result.correlationReasons, test.wantReason)
			}
			if test.wantAbsent != "" && hasResultCorrelationReason(result, test.wantAbsent) {
				t.Fatalf("reasons = %v, do not want %q", result.correlationReasons, test.wantAbsent)
			}
		})
	}
}

func TestRetransmitDropCorrelatorTakePendingRetransmits(t *testing.T) {
	correlator := newTestRetransmitDropCorrelator(t, 1)
	correlator.waitingRetransmits.capacity = 2
	now := time.Unix(30, 0)
	events := []*types.TCPRetransmitTracing{
		testRetransmitEvent(uint64(time.Second)+1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200),
		testRetransmitEvent(uint64(time.Second)+2, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 400),
		testRetransmitEvent(uint64(time.Second)+3, "10.0.0.1", "10.0.0.2", 1000, 80, 500, 600),
	}

	var emitted []retransmitDropResult
	for eventIndex, event := range events {
		results, err := correlator.processRetransmit(
			event,
			now.Add(time.Duration(eventIndex)),
		)
		if err != nil {
			t.Fatalf("processRetransmit(%d) error = %v", eventIndex, err)
		}
		emitted = append(emitted, results...)
	}
	if len(emitted) != 1 || emitted[0].retransmit != events[0] ||
		!hasResultCorrelationReason(
			emitted[0],
			types.CorrelationReasonRetransmitWaitCapacityExceeded,
		) {
		t.Fatalf("capacity results = %+v, want first event with capacity reason", emitted)
	}

	pending := correlator.takePendingRetransmits()
	if len(pending) != 2 || pending[0] != events[1] || pending[1] != events[2] {
		t.Fatalf("pending events = %+v, want remaining events in deadline order", pending)
	}
	for eventIndex, event := range pending {
		if event.DropLocation != "" || event.CorrelationReasons != nil ||
			event.DropwatchPerfStatus != nil || event.DropStack != "" {
			t.Fatalf("pending event %d correlation fields = %+v, want empty", eventIndex, event)
		}
	}
	if again := correlator.takePendingRetransmits(); len(again) != 0 {
		t.Fatalf("second pending take returned %d events, want 0", len(again))
	}
	if correlator.waitingRetransmits.byDeadline.Len() != 0 ||
		len(correlator.waitingRetransmits.byFlow) != 0 {
		t.Fatalf(
			"waiting state = deadlines %d flows %d, want empty",
			correlator.waitingRetransmits.byDeadline.Len(),
			len(correlator.waitingRetransmits.byFlow),
		)
	}
}

func TestRetransmitDropCorrelatorReportsCrossNetNSCandidate(t *testing.T) {
	correlator := newTestRetransmitDropCorrelator(t, 1)
	now := time.Unix(40, 0)
	retransmit := testRetransmitEvent(
		uint64(time.Second)+1,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	if results, err := correlator.processRetransmit(
		retransmit,
		now,
	); err != nil || len(results) != 0 {
		t.Fatalf("processRetransmit() = (%v, %v), want pending", results, err)
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
	drop.namespace.cookie = 2
	if results, err := correlator.processDrop(drop, now); err != nil || len(results) != 0 {
		t.Fatalf("processDrop() = (%v, %v), want retained candidate", results, err)
	}
	results := correlator.settleExpiredRetransmits(now.Add(retransmitDropWaitDuration))
	if len(results) != 1 || !hasResultCorrelationReason(
		results[0],
		types.CorrelationReasonCrossNetNSCandidate,
	) {
		t.Fatalf("expired result = %+v, want cross_netns_candidate", results)
	}
}

func TestRetransmitDropCorrelatorSettlesBeforeCurrentDrop(t *testing.T) {
	tests := []struct {
		name      string
		elapsed   time.Duration
		wantMatch bool
	}{
		{name: "before deadline", elapsed: retransmitDropWaitDuration - time.Nanosecond, wantMatch: true},
		{name: "at deadline", elapsed: retransmitDropWaitDuration},
		{name: "after deadline", elapsed: retransmitDropWaitDuration + time.Nanosecond},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			correlator := newTestRetransmitDropCorrelator(t, 1)
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
			if results, err := correlator.processRetransmit(retransmit, now); err != nil || len(results) != 0 {
				t.Fatalf("processRetransmit() = (%v, %v), want pending", results, err)
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
			results, err := correlator.processDrop(drop, now.Add(test.elapsed))
			if err != nil || len(results) != 1 {
				t.Fatalf("processDrop() = (%v, %v), want one result", results, err)
			}
			if test.wantMatch && results[0].drop != drop {
				t.Fatalf("result = %+v, want match", results[0])
			}
			if !test.wantMatch && (results[0].drop != nil ||
				!hasResultCorrelationReason(results[0], types.CorrelationReasonNoMatchingDrop)) {
				t.Fatalf("result = %+v, want expired no-match", results[0])
			}
			if !test.wantMatch && len(correlator.takePendingRetransmits()) != 0 {
				t.Fatal("shutdown returned the expired retransmission again")
			}
		})
	}
}

func TestRetransmitDropCorrelatorSettlesBeforeCapacityCheck(t *testing.T) {
	correlator := newTestRetransmitDropCorrelator(t, 1)
	correlator.waitingRetransmits.capacity = 1
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
	if results, err := correlator.processRetransmit(first, now); err != nil || len(results) != 0 {
		t.Fatalf("first processRetransmit() = (%v, %v), want pending", results, err)
	}
	results, err := correlator.processRetransmit(
		second,
		now.Add(retransmitDropWaitDuration),
	)
	if err != nil || len(results) != 1 || results[0].retransmit != first {
		t.Fatalf("second processRetransmit() = (%v, %v), want expired first", results, err)
	}
	if hasResultCorrelationReason(
		results[0],
		types.CorrelationReasonRetransmitWaitCapacityExceeded,
	) {
		t.Fatalf("reasons = %v, do not want capacity eviction", results[0].correlationReasons)
	}
	if correlator.waitingRetransmits.byDeadline.Len() != 1 {
		t.Fatalf("waiting retransmits = %d, want second only", correlator.waitingRetransmits.byDeadline.Len())
	}
}

func newTestRetransmitDropCorrelator(
	t *testing.T,
	readyFromKtimeNS uint64,
) *retransmitDropCorrelator {
	t.Helper()
	correlator, err := newRetransmitDropCorrelator(readyFromKtimeNS)
	if err != nil {
		t.Fatalf("newRetransmitDropCorrelator() error = %v", err)
	}
	return correlator
}

func hasCorrelationReason(
	event *types.TCPRetransmitTracing,
	reason types.CorrelationReason,
) bool {
	return slices.Contains(event.CorrelationReasons, reason)
}

func hasResultCorrelationReason(
	result retransmitDropResult,
	reason types.CorrelationReason,
) bool {
	return slices.Contains(result.correlationReasons, reason)
}

func testFlowKey(sourcePort, destinationPort uint16) flowKey {
	sourceAddress, _ := parseAddress("10.0.0.1")
	destinationAddress, _ := parseAddress("10.0.0.2")
	return flowKey{
		source: endpoint{address: sourceAddress, port: sourcePort},
		destination: endpoint{
			address: destinationAddress,
			port:    destinationPort,
		},
	}
}
