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
	"errors"
	"slices"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf/abi"
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
			drop:         &dropEvent{},
			status:       types.DropwatchStatus{PerfLost: 2},
			wantLocation: "host_software",
		},
		{
			name:         "matched with unavailable status",
			drop:         &dropEvent{},
			readErr:      statusErr,
			wantLocation: "host_software",
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
				if event.DropwatchPerfStatus != nil || event.CorrelationReasons != nil {
					t.Fatalf("matched event retained no-match fields: %+v", event)
				}
			} else if test.readErr != nil {
				if event.DropwatchPerfStatus != nil {
					t.Fatalf("unavailable status result = %+v", event)
				}
			} else if event.DropwatchPerfStatus == nil || *event.DropwatchPerfStatus != test.status {
				t.Fatalf("status = %+v, want %+v", event.DropwatchPerfStatus, test.status)
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
	if first.DropwatchPerfStatus == nil || second.DropwatchPerfStatus == nil {
		t.Fatal("batch output is missing status snapshots")
	}
	first.DropwatchPerfStatus.PerfLost = 1
	if *second.DropwatchPerfStatus != source.status {
		t.Fatalf("mutating one output changed another status snapshot: %+v", second.DropwatchPerfStatus)
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
	if event.DropwatchPerfStatus == nil ||
		event.DropwatchPerfStatus.PerfLost != 2 ||
		event.DropwatchPerfStatus.LostSamples != 5 ||
		event.DropwatchPerfStatus.RateLimited != 3 {
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
	if event.DropwatchPerfStatus != nil || !hasCorrelationReason(
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
