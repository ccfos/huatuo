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
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/dropwatch"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestEmitResultsBuildsCorrelationFields(t *testing.T) {
	statusErr := errors.New("status unavailable")
	tests := []struct {
		name         string
		drop         *dropEvent
		status       types.DropwatchStatus
		readErr      error
		wantLocation string
		wantReasons  []types.CorrelationReason
	}{
		{
			name:         "matched",
			drop:         &dropEvent{metadata: dropwatch.Metadata{Source: dropwatch.SourceSoftware}},
			status:       types.DropwatchStatus{PerfLost: 2},
			wantLocation: "software",
		},
		{
			name:         "matched with unavailable status",
			drop:         &dropEvent{metadata: dropwatch.Metadata{Source: dropwatch.SourceSoftware}},
			readErr:      statusErr,
			wantLocation: "software",
		},
		{
			name:         "unmatched",
			wantLocation: "unknown",
			wantReasons:  []types.CorrelationReason{types.CorrelationReasonNoMatchingDrop},
		},
		{
			name:         "perf output lost",
			status:       types.DropwatchStatus{PerfLost: 2},
			wantLocation: "unknown",
			wantReasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
				types.CorrelationReasonPerfEventsLost,
			},
		},
		{
			name:         "reader samples lost",
			status:       types.DropwatchStatus{LostSamples: 5},
			wantLocation: "unknown",
			wantReasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
				types.CorrelationReasonPerfEventsLost,
			},
		},
		{
			name:         "rate limited",
			status:       types.DropwatchStatus{RateLimited: 3},
			wantLocation: "unknown",
			wantReasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
				types.CorrelationReasonDropRateLimited,
			},
		},
		{
			name:         "all counters available",
			status:       types.DropwatchStatus{PerfLost: 2, LostSamples: 5, RateLimited: 3},
			wantLocation: "unknown",
			wantReasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
				types.CorrelationReasonDropRateLimited,
				types.CorrelationReasonPerfEventsLost,
			},
		},
		{
			name:         "map counters unavailable",
			readErr:      statusErr,
			wantLocation: "unknown",
			wantReasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
				types.CorrelationReasonDropwatchPerfStatusUnavailable,
			},
		},
		{
			name:         "reader loss with unavailable map counters",
			status:       types.DropwatchStatus{LostSamples: 5},
			readErr:      statusErr,
			wantLocation: "unknown",
			wantReasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
				types.CorrelationReasonDropwatchPerfStatusUnavailable,
				types.CorrelationReasonPerfEventsLost,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := &retransmitEvent{record: abi.TCPRetransmitEvent{KernelObservedNS: 42}}
			reasons := []types.CorrelationReason{types.CorrelationReasonNoMatchingDrop}
			status := &dropwatchStatusStub{status: test.status, readErr: test.readErr}
			sink := &retransmitDropWriterStub{}
			session := &retransmitDropSession{
				readDropwatchStatus: status.ReadStatus,
				sink:                sink,
				sourceType:          "tools",
			}
			err := session.emitResults([]correlationResult{{
				retransmit: input, drop: test.drop, reasons: reasons,
			}})
			if !errors.Is(err, test.readErr) {
				t.Fatalf("emit error = %v, want %v", err, test.readErr)
			}
			if len(sink.events) != 1 {
				t.Fatalf("events = %d, want 1", len(sink.events))
			}
			if status.readCalls != 1 {
				t.Fatalf("status reads = %d, want 1", status.readCalls)
			}
			event := sink.events[0]
			if event.DropLocation != test.wantLocation || event.DropStack != "" {
				t.Fatalf("emitted event = %+v", event)
			}
			if !slices.Equal(event.CorrelationReasons, test.wantReasons) {
				t.Fatalf("reasons = %v, want %v", event.CorrelationReasons, test.wantReasons)
			}
			if test.drop != nil {
				if event.DropPerfStatus != nil || event.CorrelationReasons != nil {
					t.Fatalf("matched event retained no-match fields: %+v", event)
				}
			} else if test.readErr != nil {
				if event.DropPerfStatus != nil {
					t.Fatalf("unavailable status result = %+v", event)
				}
			} else if event.DropPerfStatus == nil || *event.DropPerfStatus != test.status {
				t.Fatalf("status = %+v, want %+v", event.DropPerfStatus, test.status)
			}
			if len(reasons) != 1 || reasons[0] != types.CorrelationReasonNoMatchingDrop {
				t.Fatalf("result reasons were mutated: %v", reasons)
			}
		})
	}
}

func TestEmitResultsPreservesErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	statusErr := errors.New("status failed")
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	source.readErr = statusErr
	session := &retransmitDropSession{
		readDropwatchStatus: source.ReadStatus,
		sink:                &retransmitDropWriterStub{err: writeErr},
		sourceType:          "tools",
	}
	err := session.emitResults([]correlationResult{{
		retransmit: &retransmitEvent{},
	}})
	if !errors.Is(err, writeErr) {
		t.Fatalf("emit error = %v, want %v", err, writeErr)
	}
	if !errors.Is(err, statusErr) {
		t.Fatalf("emit error = %v, want %v", err, statusErr)
	}
}

func TestEmitResultsReadsDropwatchStatusOncePerBatch(t *testing.T) {
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	object := source
	sink := &retransmitDropWriterStub{}
	results := []correlationResult{
		{
			retransmit: &retransmitEvent{},
			reasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
			},
		},
		{
			retransmit: &retransmitEvent{},
			reasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
			},
		},
	}
	session := &retransmitDropSession{
		readDropwatchStatus: source.ReadStatus,
		sink:                sink,
		sourceType:          "tools",
	}
	if err := session.emitResults(results); err != nil {
		t.Fatalf("emitResults() error = %v", err)
	}
	// Status is queried once per batch, not once per result.
	if object.readCalls != 1 {
		t.Fatalf("perf status reads = %d, want 1", object.readCalls)
	}
	if len(sink.events) != len(results) {
		t.Fatalf("events = %d, want %d", len(sink.events), len(results))
	}
	first, second := sink.events[0], sink.events[1]
	if first.DropPerfStatus == nil || second.DropPerfStatus == nil {
		t.Fatal("batch output is missing status snapshots")
	}
	first.DropPerfStatus.PerfLost = 1
	if *second.DropPerfStatus != source.status {
		t.Fatalf("mutating one output changed another status snapshot: %+v", second.DropPerfStatus)
	}
}

func TestEmitResultsSkipsEmptyBatch(t *testing.T) {
	source := &dropwatchStatusStub{
		readErr: errors.New("unexpected status read"),
	}
	sink := &retransmitDropWriterStub{}
	session := &retransmitDropSession{
		readDropwatchStatus: source.ReadStatus,
		sink:                sink,
		sourceType:          "tools",
	}
	if err := session.emitResults(nil); err != nil {
		t.Fatalf("emitResults() error = %v", err)
	}
	if source.readCalls != 0 || len(sink.events) != 0 {
		t.Fatalf("empty batch: status reads = %d, events = %d", source.readCalls, len(sink.events))
	}
}

func TestEmitResultsUsesLatestDropwatchStatus(t *testing.T) {
	correlator := newTestEventCorrelator(t, 1)
	input := &retransmitEvent{record: abi.TCPRetransmitEvent{KernelObservedNS: uint64(maxDropToRetransmitAge) + 1}}
	result := correlator.noMatchResult(input, false)
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{
		PerfLost:    2,
		LostSamples: 5,
		RateLimited: 3,
	})
	sink := &retransmitDropWriterStub{}

	session := &retransmitDropSession{
		readDropwatchStatus: source.ReadStatus,

		sink: sink,

		sourceType: "tools",
	}

	if err := session.emitResults([]correlationResult{result}); err != nil {
		t.Fatalf("emitResults() error = %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	event := sink.events[0]
	if event.DropPerfStatus == nil ||
		event.DropPerfStatus.PerfLost != 2 ||
		event.DropPerfStatus.LostSamples != 5 ||
		event.DropPerfStatus.RateLimited != 3 {
		t.Fatalf("emitted event = %+v, want latest perf status", event)
	}
	for _, reason := range []types.CorrelationReason{
		types.CorrelationReasonPerfEventsLost,
		types.CorrelationReasonDropRateLimited,
	} {
		if !hasCorrelationReason(event, reason) {
			t.Fatalf("reasons = %v, want %q", event.CorrelationReasons, reason)
		}
	}
}

func TestEmitResultsWritesOnceWhenDropwatchStatusFails(t *testing.T) {
	statusErr := errors.New("status unavailable")
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	source.readErr = statusErr
	input := &retransmitEvent{record: abi.TCPRetransmitEvent{KernelObservedNS: 42}}
	result := correlationResult{
		retransmit: input,
		reasons: []types.CorrelationReason{
			types.CorrelationReasonNoMatchingDrop,
		},
	}
	sink := &retransmitDropWriterStub{}

	session := &retransmitDropSession{
		readDropwatchStatus: source.ReadStatus,

		sink: sink,

		sourceType: "tools",
	}

	err := session.emitResults([]correlationResult{result})
	if !errors.Is(err, statusErr) {
		t.Fatalf("emit error = %v, want %v", err, statusErr)
	}
	if len(sink.events) != 1 || sink.events[0].KernelObservedNS != input.record.KernelObservedNS {
		t.Fatalf("events = %+v, want event exactly once", sink.events)
	}
	event := sink.events[0]
	if event.DropPerfStatus != nil || !hasCorrelationReason(
		event,
		types.CorrelationReasonDropwatchPerfStatusUnavailable,
	) {
		t.Fatalf("emitted event = %+v, want unavailable status reason", event)
	}
}

type dropwatchStatusStub struct {
	status    types.DropwatchStatus
	readErr   error
	readCalls int
}

func (s *dropwatchStatusStub) ReadStatus() (types.DropwatchStatus, error) {
	s.readCalls++
	return s.status, s.readErr
}

func newTraceTestDropwatchStatus(t *testing.T, status types.DropwatchStatus) *dropwatchStatusStub {
	t.Helper()
	return &dropwatchStatusStub{status: status}
}

// ABI records pass through decoding, both arrival orders, matching and JSON.
// The kernel capture paths are exercised by the dropwatch integration tests.
func TestCorrelationPreservesDropMetadata(t *testing.T) {
	tests := []struct {
		name         string
		source       abi.DropwatchDropSource
		reason       uint32
		namespace    uint64
		wantSource   string
		wantReason   string
		wantGroup    string
		wantLocation string
	}{
		{
			name: "software", source: abi.DropwatchDropSourceSoftware, reason: 5, namespace: 1,
			wantSource: "software", wantReason: "SKB_DROP_REASON_TCP_CSUM", wantLocation: "software",
		},
		{
			name: "unknown reason", source: abi.DropwatchDropSourceSoftware, reason: 999, namespace: 1,
			wantSource: "software", wantReason: "999", wantLocation: "software",
		},
		{
			name: "unsupported kernel", source: abi.DropwatchDropSourceSoftware, reason: ^uint32(0), namespace: 1,
			wantSource: "software", wantReason: "NOT_SUPPORTED", wantLocation: "software",
		},
		{
			name: "hardware", source: abi.DropwatchDropSourceHardware, namespace: 1,
			wantSource: "hardware", wantReason: "ingress_vlan_filter", wantGroup: "l2_drops", wantLocation: "hardware",
		},
		{
			name: "unknown source", source: abi.DropwatchDropSourceUnknown, reason: 999, namespace: 1,
			wantSource: "unknown", wantReason: "999", wantLocation: "unknown",
		},
		{
			name: "hardware across namespaces", source: abi.DropwatchDropSourceHardware, namespace: 2,
			wantLocation: "unknown",
		},
		{
			name: "hardware missing namespace", source: abi.DropwatchDropSourceHardware,
			wantLocation: "unknown",
		},
	}
	for i := range tests {
		test := &tests[i]
		t.Run(test.name, func(t *testing.T) {
			for _, order := range []string{"drop first", "retransmit first"} {
				t.Run(order, func(t *testing.T) {
					record := newIPv4DropwatchTCPRecord(140)
					record.Meta.KernelObservedNS = uint64(2 * time.Second)
					record.Meta.NetNamespaceCookie = test.namespace
					record.Meta.DropSource = uint32(test.source)
					record.Meta.DropReason = test.reason
					copy(record.Meta.TrapName[:], "ingress_vlan_filter")
					copy(record.Meta.TrapGroupName[:], "l2_drops")
					drop, err := dropEventFromRecord(record, dropwatch.ReasonNames{5: "SKB_DROP_REASON_TCP_CSUM"})
					if err != nil {
						t.Fatal(err)
					}
					// The perf reader reuses the complete record before output.
					*record = abi.DropwatchPacketEvent{}
					retransmit := testRetransmitEvent(uint64(2*time.Second)+1, "10.0.0.1", "10.0.0.2", 12345, 80, 123, 223)
					correlator := newTestEventCorrelator(t, 1)
					now := time.Unix(10, 0)
					var results []correlationResult
					if order == "drop first" {
						results = append(results, correlator.processDropEvent(drop, now)...)
						results = append(results, correlator.processRetransmitEvent(retransmit, now)...)
					} else {
						results = append(results, correlator.processRetransmitEvent(retransmit, now)...)
						results = append(results, correlator.processDropEvent(drop, now)...)
					}
					results = append(results, correlator.settleAllRetransmits()...)
					var output bytes.Buffer
					session := retransmitDropSession{
						readDropwatchStatus: (&dropwatchStatusStub{}).ReadStatus,
						sink:                &jsonWriter{w: &output},
					}
					if err := session.emitResults(results); err != nil {
						t.Fatal(err)
					}
					var event types.TCPRetransmitTracing
					if err := json.Unmarshal(output.Bytes(), &event); err != nil {
						t.Fatal(err)
					}
					if event.DropSource != test.wantSource || event.DropReason != test.wantReason ||
						event.DropReasonGroup != test.wantGroup || event.DropLocation != test.wantLocation {
						t.Fatalf("drop output = (%q, %q, %q, %q), want (%q, %q, %q, %q)",
							event.DropSource, event.DropReason, event.DropReasonGroup, event.DropLocation,
							test.wantSource, test.wantReason, test.wantGroup, test.wantLocation)
					}
					if test.namespace != 1 && !hasCorrelationReason(&event, types.CorrelationReasonNoMatchingDrop) {
						t.Fatalf("unmatched hardware event lacks no-match reason: %+v", event)
					}
					if test.namespace == 1 && len(event.CorrelationReasons) != 0 {
						t.Fatalf("matched drop has correlation reasons: %v", event.CorrelationReasons)
					}
				})
			}
		})
	}
}
