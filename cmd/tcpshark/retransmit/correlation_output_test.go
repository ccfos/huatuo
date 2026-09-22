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
	"strings"
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
		reason       types.CorrelationReason
		drop         *dropEvent
		status       types.DropwatchStatus
		readErr      error
		wantLocation string
		netNamespace bool
	}{
		{
			name: "matched", reason: types.CorrelationMatched,
			drop:   &dropEvent{metadata: dropwatch.Metadata{Source: dropwatch.SourceSoftware}},
			status: types.DropwatchStatus{PerfLost: 2}, wantLocation: "software",
		},
		{
			name: "matched unknown source", reason: types.CorrelationMatched,
			drop:         &dropEvent{metadata: dropwatch.Metadata{Source: dropwatch.SourceUnknown}},
			wantLocation: "unknown",
		},
		{
			name: "matched with unavailable status", reason: types.CorrelationMatched,
			drop:    &dropEvent{metadata: dropwatch.Metadata{Source: dropwatch.SourceSoftware}},
			readErr: statusErr, wantLocation: "software",
		},
		{name: "unmatched", reason: types.CorrelationWaitTimeout, wantLocation: "unknown"},
		{name: "warmup", reason: types.CorrelationWarmup, wantLocation: "unknown"},
		{
			name: "warmup with partial status", reason: types.CorrelationWarmup,
			status: types.DropwatchStatus{LostSamples: 5}, readErr: statusErr, wantLocation: "unknown",
			netNamespace: true,
		},
		{
			name: "namespace matched without packet match", reason: types.CorrelationWaitTimeout,
			wantLocation: "unknown", netNamespace: true,
		},
		{
			name: "perf output lost", reason: types.CorrelationWaitTimeout,
			status: types.DropwatchStatus{PerfLost: 2}, wantLocation: "unknown",
		},
		{
			name: "reader samples lost", reason: types.CorrelationWaitTimeout,
			status: types.DropwatchStatus{LostSamples: 5}, wantLocation: "unknown",
		},
		{
			name: "rate limited", reason: types.CorrelationWaitTimeout,
			status: types.DropwatchStatus{RateLimited: 3}, wantLocation: "unknown",
		},
		{
			name: "all counters available", reason: types.CorrelationWaitTimeout,
			status: types.DropwatchStatus{PerfLost: 2, LostSamples: 5, RateLimited: 3}, wantLocation: "unknown",
		},
		{
			name: "map counters unavailable", reason: types.CorrelationWaitTimeout,
			readErr: statusErr, wantLocation: "unknown",
		},
		{
			name: "reader loss with unavailable map counters", reason: types.CorrelationWaitTimeout,
			status: types.DropwatchStatus{LostSamples: 5}, readErr: statusErr, wantLocation: "unknown",
		},
		{
			name: "unsupported with loss and rate limiting", reason: types.CorrelationUnsupported,
			status: types.DropwatchStatus{PerfLost: 2, LostSamples: 5, RateLimited: 3}, wantLocation: "unknown",
		},
		{
			name: "queue full with loss and rate limiting", reason: types.CorrelationQueueFull,
			status: types.DropwatchStatus{PerfLost: 2, LostSamples: 5, RateLimited: 3}, wantLocation: "unknown",
		},
		{
			name: "interrupted with unavailable counters", reason: types.CorrelationInterrupted,
			status: types.DropwatchStatus{LostSamples: 5}, readErr: statusErr, wantLocation: "unknown",
		},
	}
	for testIndex := range tests {
		test := &tests[testIndex]
		t.Run(test.name, func(t *testing.T) {
			input := &retransmitEvent{record: abi.TCPRetransmitEvent{KernelObservedNS: 42}}
			test.status.HasMapCounters = test.readErr == nil
			source := &dropwatchStatusStub{status: test.status, readErr: test.readErr}
			sink := &retransmitDropWriterStub{}
			session := &retransmitDropSession{readDropwatchStatus: source.ReadStatus, sink: sink, sourceType: "tools"}
			result := correlationResult{
				retransmit: input, drop: test.drop, reason: test.reason,
				netNamespace: test.reason == types.CorrelationMatched || test.netNamespace,
			}
			err := session.emitResults([]correlationResult{result})
			if !errors.Is(err, test.readErr) {
				t.Fatalf("emit error = %v, want %v", err, test.readErr)
			}
			if len(sink.events) != 1 || source.readCalls != 1 {
				t.Fatalf("events = %d, reads = %d, want one each", len(sink.events), source.readCalls)
			}
			event := sink.events[0]
			if event.DropLocation != test.wantLocation || event.DropStack != "" || event.CorrelationReason != test.reason {
				t.Fatalf("emitted event = %+v, want location=%q reason=%q", event, test.wantLocation, test.reason)
			}
			if event.NetNamespace != result.netNamespace {
				t.Fatalf("diagnostics changed during output: %+v", event)
			}
			if test.reason == types.CorrelationMatched {
				if event.DropPerfStatus != nil {
					t.Fatalf("matched event retained unmatched diagnostics: %+v", event)
				}
			} else if event.DropPerfStatus == nil || *event.DropPerfStatus != test.status {
				t.Fatalf("status = %+v, want %+v", event.DropPerfStatus, test.status)
			}
		})
	}
}

func TestEmitResultsRejectsInvalidCorrelationResult(t *testing.T) {
	for _, test := range []struct {
		name      string
		reason    types.CorrelationReason
		drop      *dropEvent
		wantError string
	}{
		{name: "missing reason", wantError: "invalid reason"},
		{name: "unknown reason", reason: "unexpected", wantError: "invalid reason"},
		{name: "matched without drop", reason: types.CorrelationMatched, wantError: "requires a drop"},
		{name: "unsupported with drop", reason: types.CorrelationUnsupported, drop: &dropEvent{}, wantError: "cannot include a drop"},
		{name: "warmup with drop", reason: types.CorrelationWarmup, drop: &dropEvent{}, wantError: "cannot include a drop"},
		{name: "timeout with drop", reason: types.CorrelationWaitTimeout, drop: &dropEvent{}, wantError: "cannot include a drop"},
		{name: "queue full with drop", reason: types.CorrelationQueueFull, drop: &dropEvent{}, wantError: "cannot include a drop"},
		{name: "interrupted with drop", reason: types.CorrelationInterrupted, drop: &dropEvent{}, wantError: "cannot include a drop"},
	} {
		t.Run(test.name, func(t *testing.T) {
			statusErr := errors.New("status unavailable")
			source := &dropwatchStatusStub{readErr: statusErr}
			sink := &retransmitDropWriterStub{}
			session := retransmitDropSession{readDropwatchStatus: source.ReadStatus, sink: sink}
			err := session.emitResults([]correlationResult{{retransmit: &retransmitEvent{}, drop: test.drop, reason: test.reason}})
			if err == nil || !strings.Contains(err.Error(), test.wantError) || !errors.Is(err, statusErr) {
				t.Fatalf("emit error = %v, want %q and status error", err, test.wantError)
			}
			if len(sink.events) != 0 {
				t.Fatalf("invalid result reached output: %+v", sink.events)
			}
		})
	}
}

func TestEmitResultsPreservesErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	statusErr := errors.New("status failed")
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	source.readErr = statusErr
	source.status.HasMapCounters = false
	session := &retransmitDropSession{
		readDropwatchStatus: source.ReadStatus,
		sink:                &retransmitDropWriterStub{err: writeErr},
		sourceType:          "tools",
	}
	err := session.emitResults([]correlationResult{{
		retransmit: &retransmitEvent{},
		reason:     types.CorrelationWaitTimeout,
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
			reason:     types.CorrelationWaitTimeout,
		},
		{
			retransmit: &retransmitEvent{},
			reason:     types.CorrelationWaitTimeout,
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
	input := &retransmitEvent{record: abi.TCPRetransmitEvent{KernelObservedNS: uint64(maxDropToRetransmitAge) + 1}}
	result := correlationResult{
		retransmit: input,
		reason:     types.CorrelationWaitTimeout,
	}
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
	if event.CorrelationReason != types.CorrelationWaitTimeout || !event.DropPerfStatus.HasMapCounters {
		t.Fatalf("emitted event = %+v, want wait_timeout with available map counters", event)
	}
}

func TestEmitResultsWritesOnceWhenDropwatchStatusFails(t *testing.T) {
	statusErr := errors.New("status unavailable")
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	source.readErr = statusErr
	source.status.HasMapCounters = false
	input := &retransmitEvent{record: abi.TCPRetransmitEvent{KernelObservedNS: 42}}
	result := correlationResult{
		retransmit: input,
		reason:     types.CorrelationWaitTimeout,
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
	if event.DropPerfStatus == nil || event.DropPerfStatus.HasMapCounters ||
		event.CorrelationReason != types.CorrelationWaitTimeout {
		t.Fatalf("emitted event = %+v, want wait_timeout with unavailable map counters", event)
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
	status.HasMapCounters = true
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
					results = append(results, correlator.drainRetransmits(now)...)
					var output bytes.Buffer
					session := retransmitDropSession{
						readDropwatchStatus: newTraceTestDropwatchStatus(t, types.DropwatchStatus{}).ReadStatus,
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
					if test.namespace != 1 && event.CorrelationReason != types.CorrelationInterrupted {
						t.Fatalf("unmatched hardware event lacks interruption reason: %+v", event)
					}
					if test.namespace == 1 && event.CorrelationReason != types.CorrelationMatched {
						t.Fatalf("matched drop has reason %q", event.CorrelationReason)
					}
					if event.NetNamespace != (test.namespace == 1) {
						t.Fatalf("matched namespace = %t, want %t", event.NetNamespace, test.namespace == 1)
					}
				})
			}
		})
	}
}
