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

package netcorrelate

import (
	"encoding/binary"
	"slices"
	"sync"
	"testing"

	"huatuo-bamai/internal/packet"
	"huatuo-bamai/pkg/types"
)

func TestCorrelatorDropFirst(t *testing.T) {
	c := newTestCorrelator(t, 1)
	drop := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	if results, err := c.AddDrop(drop); err != nil || len(results) != 0 {
		t.Fatalf("AddDrop = (%v, %v)", results, err)
	}
	retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	results, err := c.AddRetrans(retrans)
	if err != nil {
		t.Fatalf("AddRetrans: %v", err)
	}
	assertMatchedResult(t, results, retrans, drop)
}

func TestCorrelatorRetransFirst(t *testing.T) {
	c := newTestCorrelator(t, 1)
	retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	if results, err := c.AddRetrans(retrans); err != nil || len(results) != 0 {
		t.Fatalf("AddRetrans = (%v, %v), want pending", results, err)
	}
	drop := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	results, err := c.AddDrop(drop)
	if err != nil {
		t.Fatalf("AddDrop: %v", err)
	}
	assertMatchedResult(t, results, retrans, drop)
}

func TestCorrelatorPerfStatusSealsNoMatch(t *testing.T) {
	c := newTestCorrelator(t, 10)
	retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	if results, err := c.AddRetrans(retrans); err != nil || len(results) != 0 {
		t.Fatalf("AddRetrans = (%v, %v), want pending", results, err)
	}
	status := types.DropwatchPerfStatus{
		DrainedThroughKtimeNS: 20,
		PerfLost:              1,
		RateLimited:           2,
	}
	results, err := c.UpdatePerfStatus(status)
	if err != nil {
		t.Fatalf("UpdatePerfStatus: %v", err)
	}
	assertNoMatchResult(t, results, retrans, status, []types.CorrelationReason{
		types.CorrelationReasonStartupHistoryIncomplete,
		types.CorrelationReasonPerfEventsLost,
		types.CorrelationReasonDropRateLimited,
	})
}

func TestCorrelatorStatusBeforeRetrans(t *testing.T) {
	c := newTestCorrelator(t, 10)
	status := types.DropwatchPerfStatus{DrainedThroughKtimeNS: 30}
	if results, err := c.UpdatePerfStatus(status); err != nil || len(results) != 0 {
		t.Fatalf("UpdatePerfStatus = (%v, %v)", results, err)
	}
	retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	results, err := c.AddRetrans(retrans)
	if err != nil {
		t.Fatalf("AddRetrans: %v", err)
	}
	assertNoMatchResult(t, results, retrans, status, []types.CorrelationReason{
		types.CorrelationReasonStartupHistoryIncomplete,
	})
}

func TestCorrelatorHealthyUnsupportedRetransmissionStaysUnknown(t *testing.T) {
	c := newTestCorrelator(t, 10)
	status := types.DropwatchPerfStatus{DrainedThroughKtimeNS: 30}
	if _, err := c.UpdatePerfStatus(status); err != nil {
		t.Fatalf("UpdatePerfStatus: %v", err)
	}
	retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	retrans.EventType = "tcp_send_loss_probe"
	results, err := c.AddRetrans(retrans)
	if err != nil {
		t.Fatalf("AddRetrans: %v", err)
	}
	if len(results) != 1 || results[0].Retrans != retrans {
		t.Fatalf("results = %+v, want one retransmission", results)
	}
	if retrans.DropLocation != "unknown" {
		t.Fatalf("drop location = %q, want unknown", retrans.DropLocation)
	}
	if retrans.DropwatchPerfStatus == nil ||
		retrans.DropwatchPerfStatus.DrainedThroughKtimeNS != status.DrainedThroughKtimeNS {
		t.Fatalf("perf status = %+v, want %+v", retrans.DropwatchPerfStatus, status)
	}
	assertCorrelationReasons(t, retrans, []types.CorrelationReason{
		types.CorrelationReasonStartupHistoryIncomplete,
		types.CorrelationReasonUnsupportedRetransmission,
	})
}

func TestCorrelatorHealthyNoMatchStaysUnknownAfterEvidenceEviction(t *testing.T) {
	c := newTestCorrelator(t, 1)
	c.matcher.capacity = 1
	if _, err := c.AddDrop(testDrop(
		10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140,
	)); err != nil {
		t.Fatalf("AddDrop(first): %v", err)
	}
	if _, err := c.AddDrop(testDrop(
		11, "10.0.0.1", "10.0.0.2", 1000, 80, 1000, 0, packet.TCPFlagACK, 140,
	)); err != nil {
		t.Fatalf("AddDrop(second): %v", err)
	}
	status := types.DropwatchPerfStatus{DrainedThroughKtimeNS: 30}
	if _, err := c.UpdatePerfStatus(status); err != nil {
		t.Fatalf("UpdatePerfStatus: %v", err)
	}
	retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	results, err := c.AddRetrans(retrans)
	if err != nil {
		t.Fatalf("AddRetrans: %v", err)
	}
	if len(results) != 1 || retrans.DropLocation != "unknown" {
		t.Fatalf("results = %+v, want one unknown retransmission", results)
	}
	if retrans.DropwatchPerfStatus == nil ||
		retrans.DropwatchPerfStatus.DrainedThroughKtimeNS != status.DrainedThroughKtimeNS {
		t.Fatalf("perf status = %+v, want %+v", retrans.DropwatchPerfStatus, status)
	}
	assertCorrelationReasons(t, retrans, []types.CorrelationReason{
		types.CorrelationReasonStartupHistoryIncomplete,
		types.CorrelationReasonDropEvidenceEvicted,
	})
}

func TestCorrelatorCrossNetNSCandidateStaysUnknown(t *testing.T) {
	c := newTestCorrelator(t, 1)
	drop := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	drop.NetNamespaceCookie = 2
	drop.NetNamespaceInode = 3
	if _, err := c.AddDrop(drop); err != nil {
		t.Fatalf("AddDrop: %v", err)
	}
	status := types.DropwatchPerfStatus{DrainedThroughKtimeNS: 30}
	if _, err := c.UpdatePerfStatus(status); err != nil {
		t.Fatalf("UpdatePerfStatus: %v", err)
	}

	retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	results, err := c.AddRetrans(retrans)
	if err != nil {
		t.Fatalf("AddRetrans: %v", err)
	}
	assertNoMatchResult(t, results, retrans, status, []types.CorrelationReason{
		types.CorrelationReasonStartupHistoryIncomplete,
		types.CorrelationReasonCrossNetNSCandidate,
	})
}

func TestCorrelatorSameNetNSMatchWinsOverCrossNetNSCandidate(t *testing.T) {
	c := newTestCorrelator(t, 1)
	crossNetNS := testDrop(9, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	crossNetNS.NetNamespaceCookie = 2
	crossNetNS.NetNamespaceInode = 3
	sameNetNS := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	for _, drop := range []*DropEvent{crossNetNS, sameNetNS} {
		if _, err := c.AddDrop(drop); err != nil {
			t.Fatalf("AddDrop: %v", err)
		}
	}

	retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	results, err := c.AddRetrans(retrans)
	if err != nil {
		t.Fatalf("AddRetrans: %v", err)
	}
	assertMatchedResult(t, results, retrans, sameNetNS)
}

func TestCorrelatorUnusableDeliveredDropContaminatesCoverage(t *testing.T) {
	tests := []struct {
		name   string
		record func() *DropEvent
	}{
		{
			name: "partial IPv4 packet",
			record: func() *DropEvent {
				record := newIPv4DropwatchTCPRecord(40, 40)
				record.PktHdr.RawLen = 20
				record.Meta.KtimeNS = 10
				record.Meta.NetNSCookie = 1
				event, _ := DropEventFromRecord(record)
				return event
			},
		},
		{
			name: "non-initial IPv4 fragment",
			record: func() *DropEvent {
				record := newIPv4DropwatchTCPRecord(40, 40)
				binary.BigEndian.PutUint16(record.PktHdr.Raw[6:], 1)
				record.Meta.KtimeNS = 10
				record.Meta.NetNSCookie = 1
				event, _ := DropEventFromRecord(record)
				return event
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCorrelator(t, 1)
			drop := tt.record()
			if drop == nil || drop.Layers == nil || drop.Layers.TCP != nil {
				t.Fatalf("drop layers = %+v, want partial IPv4 without TCP", drop)
			}
			if results, err := c.AddDrop(drop); err != nil || len(results) != 0 {
				t.Fatalf("AddDrop = (%v, %v)", results, err)
			}
			status := types.DropwatchPerfStatus{DrainedThroughKtimeNS: 30}
			if _, err := c.UpdatePerfStatus(status); err != nil {
				t.Fatalf("UpdatePerfStatus: %v", err)
			}
			retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
			results, err := c.AddRetrans(retrans)
			if err != nil {
				t.Fatalf("AddRetrans: %v", err)
			}
			assertNoMatchResult(t, results, retrans, status, []types.CorrelationReason{
				types.CorrelationReasonStartupHistoryIncomplete,
				types.CorrelationReasonDropEvidenceUnusable,
			})
		})
	}
}

func TestCorrelatorNoMatchReportsCoverageStatus(t *testing.T) {
	tests := []struct {
		name        string
		ready       uint64
		ktime       uint64
		endBefore   bool
		wantKtime   uint64
		wantReasons []types.CorrelationReason
	}{
		{
			name: "before ready", ready: 30, ktime: 20, wantKtime: 100,
			wantReasons: []types.CorrelationReason{
				types.CorrelationReasonStartupHistoryIncomplete,
				types.CorrelationReasonPerfEventsLost,
				types.CorrelationReasonDropRateLimited,
			},
		},
		{
			name: "zero ktime", ready: 10, ktime: 0, wantKtime: 100,
			wantReasons: []types.CorrelationReason{
				types.CorrelationReasonStartupHistoryIncomplete,
				types.CorrelationReasonPerfEventsLost,
				types.CorrelationReasonDropRateLimited,
				types.CorrelationReasonUnsupportedRetransmission,
			},
		},
		{
			name: "end input", ready: 10, ktime: 20, endBefore: true,
			wantKtime: 0,
			wantReasons: []types.CorrelationReason{
				types.CorrelationReasonDropwatchInputInactive,
				types.CorrelationReasonStartupHistoryIncomplete,
				types.CorrelationReasonPerfFrontierIncomplete,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCorrelator(t, tt.ready)
			if _, err := c.UpdatePerfStatus(types.DropwatchPerfStatus{
				DrainedThroughKtimeNS: 100, PerfLost: 3, RateLimited: 4,
			}); err != nil {
				t.Fatalf("UpdatePerfStatus: %v", err)
			}
			retrans := testRetrans(tt.ktime, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
			if tt.endBefore {
				// A fresh one-shot correlator has no drained frontier.
				c = newTestCorrelator(t, tt.ready)
				results, err := c.AddRetrans(retrans)
				if err != nil || len(results) != 0 {
					t.Fatalf("AddRetrans = (%v, %v), want pending", results, err)
				}
				results = c.EndDropwatchInput()
				assertNoMatchResult(
					t, results, retrans, types.DropwatchPerfStatus{}, tt.wantReasons,
				)
				return
			}
			results, err := c.AddRetrans(retrans)
			if err != nil {
				t.Fatalf("AddRetrans: %v", err)
			}
			if len(results) != 1 || results[0].Retrans.DropwatchPerfStatus == nil {
				t.Fatalf("results = %+v", results)
			}
			got := results[0].Retrans.DropwatchPerfStatus
			if got.DrainedThroughKtimeNS != tt.wantKtime || got.PerfLost != 3 || got.RateLimited != 4 {
				t.Fatalf("status = %+v, want frontier=%d counters=3/4", got, tt.wantKtime)
			}
			assertCorrelationReasons(t, retrans, tt.wantReasons)
		})
	}
}

func TestCorrelatorEndIsIdempotentAndRetainsMatchedDrops(t *testing.T) {
	c := newTestCorrelator(t, 1)
	drop := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	if _, err := c.AddDrop(drop); err != nil {
		t.Fatalf("AddDrop: %v", err)
	}
	if results := c.EndDropwatchInput(); len(results) != 0 {
		t.Fatalf("first EndDropwatchInput = %+v", results)
	}
	if results := c.EndDropwatchInput(); len(results) != 0 {
		t.Fatalf("second EndDropwatchInput = %+v", results)
	}
	retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	results, err := c.AddRetrans(retrans)
	if err != nil {
		t.Fatalf("AddRetrans after End: %v", err)
	}
	assertMatchedResult(t, results, retrans, drop)
}

func TestCorrelatorPendingCapacityDoesNotDropRetransmissions(t *testing.T) {
	c := newTestCorrelator(t, 1)

	inputs := make([]*types.TCPRetransmitTracing, 0, pendingCapacity+1)
	seen := make(map[*types.TCPRetransmitTracing]int, pendingCapacity+1)
	for i := 0; i <= pendingCapacity; i++ {
		event := testRetrans(
			uint64(i+1),
			"10.0.0.1",
			"10.0.0.2",
			1000,
			80,
			uint32(i*100+1),
			uint32(i*100+101),
		)
		inputs = append(inputs, event)
		results, err := c.AddRetrans(event)
		if err != nil {
			t.Fatalf("AddRetrans(%d): %v", i, err)
		}
		for _, result := range results {
			seen[result.Retrans]++
		}
	}
	for _, result := range c.EndDropwatchInput() {
		seen[result.Retrans]++
	}
	if len(seen) != pendingCapacity+1 {
		t.Fatalf("output retransmissions = %d, want %d", len(seen), pendingCapacity+1)
	}
	for i, event := range inputs {
		if seen[event] != 1 {
			t.Fatalf("output count for retransmission %d = %d, want 1", i, seen[event])
		}
	}
	assertCorrelationReasons(t, inputs[0], []types.CorrelationReason{
		types.CorrelationReasonStartupHistoryIncomplete,
		types.CorrelationReasonPerfFrontierIncomplete,
		types.CorrelationReasonPendingCapacityExceeded,
	})
	if results := c.EndDropwatchInput(); len(results) != 0 {
		t.Fatalf("second EndDropwatchInput = %+v, want no duplicate output", results)
	}
}

func TestCorrelatorUnsupportedRetransmissionsDoNotDisplaceSupportedPending(t *testing.T) {
	c := newTestCorrelator(t, 1)
	supported := testRetrans(
		20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200,
	)
	if results, err := c.AddRetrans(supported); err != nil || len(results) != 0 {
		t.Fatalf("AddRetrans(supported) = (%v, %v), want pending", results, err)
	}

	for i := 0; i <= pendingCapacity; i++ {
		unsupported := testRetrans(
			uint64(21+i), "10.0.0.1", "10.0.0.2", 1000, 80,
			uint32(1000+i), uint32(1100+i),
		)
		unsupported.EventType = "tcp_send_loss_probe"
		results, err := c.AddRetrans(unsupported)
		if err != nil {
			t.Fatalf("AddRetrans(unsupported %d): %v", i, err)
		}
		if len(results) != 1 || results[0].Retrans != unsupported {
			t.Fatalf("AddRetrans(unsupported %d) = %+v, want immediate result", i, results)
		}
		assertCorrelationReasons(t, unsupported, []types.CorrelationReason{
			types.CorrelationReasonStartupHistoryIncomplete,
			types.CorrelationReasonPerfFrontierIncomplete,
			types.CorrelationReasonUnsupportedRetransmission,
		})
	}

	drop := testDrop(
		10, "10.0.0.1", "10.0.0.2", 1000, 80,
		100, 0, packet.TCPFlagACK, 140,
	)
	results, err := c.AddDrop(drop)
	if err != nil {
		t.Fatalf("AddDrop: %v", err)
	}
	assertMatchedResult(t, results, supported, drop)
}

func TestCorrelatorRejectsProtocolErrors(t *testing.T) {
	if c, err := NewCorrelator(0); err == nil || c != nil {
		t.Fatalf("NewCorrelator(0) = (%v, %v), want nil correlator and error", c, err)
	}
	c := newTestCorrelator(t, 1)
	c.EndDropwatchInput()
	if _, err := c.AddDrop(testDrop(
		1, "10.0.0.1", "10.0.0.2", 1, 2, 1, 0, packet.TCPFlagSYN, 40,
	)); err == nil {
		t.Fatal("AddDrop after EndDropwatchInput error = nil")
	}
	if _, err := c.UpdatePerfStatus(types.DropwatchPerfStatus{}); err == nil {
		t.Fatal("UpdatePerfStatus after EndDropwatchInput error = nil")
	}

	c = newTestCorrelator(t, 1)
	if _, err := c.UpdatePerfStatus(types.DropwatchPerfStatus{DrainedThroughKtimeNS: 10, PerfLost: 2}); err != nil {
		t.Fatalf("UpdatePerfStatus: %v", err)
	}
	if _, err := c.UpdatePerfStatus(types.DropwatchPerfStatus{DrainedThroughKtimeNS: 9, PerfLost: 2}); err == nil {
		t.Fatal("regressed frontier error = nil")
	}
	if _, err := c.UpdatePerfStatus(types.DropwatchPerfStatus{DrainedThroughKtimeNS: 10, PerfLost: 1}); err == nil {
		t.Fatal("regressed perf_lost error = nil")
	}
}

func TestCorrelatorZeroValueIsInactive(t *testing.T) {
	var c Correlator
	if _, err := c.AddDrop(testDrop(
		1, "10.0.0.1", "10.0.0.2", 1, 2, 1, 0, packet.TCPFlagSYN, 40,
	)); err == nil {
		t.Fatal("zero-value AddDrop error = nil")
	}
	if _, err := c.UpdatePerfStatus(types.DropwatchPerfStatus{}); err == nil {
		t.Fatal("zero-value UpdatePerfStatus error = nil")
	}
	if results := c.EndDropwatchInput(); len(results) != 0 {
		t.Fatalf("zero-value EndDropwatchInput = %+v, want no results", results)
	}
}

func TestCorrelatorConcurrentInput(t *testing.T) {
	c := newTestCorrelator(t, 1)
	const events = 64
	var wg sync.WaitGroup
	for i := range events {
		seq := uint32(i*100 + 1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = c.AddDrop(testDrop(
				uint64(i+1), "10.0.0.1", "10.0.0.2", 1000, 80,
				seq, 0, packet.TCPFlagACK, 140,
			))
		}()
		go func() {
			defer wg.Done()
			_, _ = c.AddRetrans(testRetrans(uint64(events+i+1), "10.0.0.1", "10.0.0.2", 1000, 80, seq, seq+100))
		}()
	}
	wg.Wait()
	if _, err := c.UpdatePerfStatus(types.DropwatchPerfStatus{DrainedThroughKtimeNS: 1000}); err != nil {
		t.Fatalf("UpdatePerfStatus: %v", err)
	}
	c.EndDropwatchInput()
}

func newTestCorrelator(t *testing.T, readyFromKtimeNS uint64) *Correlator {
	t.Helper()
	c, err := NewCorrelator(readyFromKtimeNS)
	if err != nil {
		t.Fatalf("NewCorrelator(%d): %v", readyFromKtimeNS, err)
	}
	return c
}

func assertMatchedResult(
	t *testing.T,
	results []CorrelationResult,
	retrans *types.TCPRetransmitTracing,
	drop *DropEvent,
) {
	t.Helper()
	if len(results) != 1 || results[0].Retrans != retrans || results[0].Drop != drop {
		t.Fatalf("results = %+v, want retrans=%p drop=%p", results, retrans, drop)
	}
	if retrans.DropLocation != "host_software" || retrans.DropwatchPerfStatus != nil ||
		retrans.CorrelationReasons != nil {
		t.Fatalf("matched retrans = %+v", retrans)
	}
}

func assertNoMatchResult(
	t *testing.T,
	results []CorrelationResult,
	retrans *types.TCPRetransmitTracing,
	want types.DropwatchPerfStatus,
	wantReasons []types.CorrelationReason,
) {
	t.Helper()
	if len(results) != 1 || results[0].Retrans != retrans || results[0].Drop != nil {
		t.Fatalf("results = %+v, want one no-match", results)
	}
	got := retrans.DropwatchPerfStatus
	if retrans.DropLocation != "unknown" || got == nil {
		t.Fatalf("no-match retrans = %+v", retrans)
	}
	if got.PerfLost != want.PerfLost || got.RateLimited != want.RateLimited {
		t.Fatalf("status = %+v, want counters %+v", got, want)
	}
	if got.DrainedThroughKtimeNS != want.DrainedThroughKtimeNS {
		t.Fatalf("status = %+v, want frontier %+v", got, want)
	}
	assertCorrelationReasons(t, retrans, wantReasons)
}

func assertCorrelationReasons(
	t *testing.T,
	retrans *types.TCPRetransmitTracing,
	want []types.CorrelationReason,
) {
	t.Helper()
	if !slices.Equal(retrans.CorrelationReasons, want) {
		t.Fatalf("correlation reasons = %v, want %v", retrans.CorrelationReasons, want)
	}
}
