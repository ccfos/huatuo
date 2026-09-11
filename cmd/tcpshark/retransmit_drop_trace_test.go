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
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/pkg/types"
)

type retransmitDropWriterStub struct {
	err        error
	events     []*types.TCPRetransmitTracing
	operations []string
}

func (s *retransmitDropWriterStub) Write(event *types.TCPRetransmitTracing) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, event)
	s.operations = append(s.operations, "write")
	return nil
}

func (s *retransmitDropWriterStub) close() error {
	s.operations = append(s.operations, "output_close")
	return nil
}

func TestEventSourceErrorCancelsSiblingWorkers(t *testing.T) {
	group, groupCtx := errgroup.WithContext(t.Context())
	sourceErr := errors.New("source failed")
	events := startEventSource[int](group, func(chan<- int) error {
		return sourceErr
	})
	siblingStopped := make(chan struct{})
	group.Go(func() error {
		<-groupCtx.Done()
		close(siblingStopped)
		return nil
	})

	err := group.Wait()
	if !errors.Is(err, sourceErr) {
		t.Fatalf("group.Wait() error = %v, want %v", err, sourceErr)
	}
	if _, isOpen := <-events; isOpen {
		t.Fatal("event source channel remained open")
	}
	select {
	case <-siblingStopped:
	default:
		t.Fatal("source error did not stop sibling worker")
	}
}

func TestRetransmitDropCancellationFinalizesPendingEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	retransmitEvents := make(chan *types.TCPRetransmitTracing)
	dropwatchEvents := make(chan *dropEvent)
	sink := &retransmitDropWriterStub{}
	source := newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{})
	done := make(chan error, 1)
	go func() {
		done <- runRetransmitWithDrop(ctx, &retransmitDropSession{
			retransmitEvents: retransmitEvents,
			dropwatchEvents:  dropwatchEvents,
			dropwatchSource:  source,
			sink:             sink,
		})
	}()

	retransmit := testRetransmitEvent(
		uint64(time.Second)+1,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	retransmit.DropLocation = "stale"
	retransmit.CorrelationReasons = []types.CorrelationReason{
		types.CorrelationReasonNoMatchingDrop,
	}
	retransmit.DropwatchPerfStatus = &types.DropwatchPerfStatus{PerfLost: 1}
	retransmit.DropStack = "stale"
	retransmitEvents <- retransmit
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("runRetransmitWithDrop() error = %v", err)
	}
	if len(sink.events) != 1 || sink.events[0] != retransmit {
		t.Fatalf("events = %+v, want pending retransmission exactly once", sink.events)
	}
	if retransmit.DropLocation != "unknown" ||
		!hasCorrelationReason(retransmit, types.CorrelationReasonNoMatchingDrop) ||
		retransmit.DropwatchPerfStatus == nil || retransmit.DropStack != "" {
		t.Fatalf("finalized retransmission = %+v, want unknown no-match result", retransmit)
	}
}

func TestRetransmitDropCancellationSettlesCrossNetNSCandidate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	retransmitEvents := make(chan *types.TCPRetransmitTracing)
	dropwatchEvents := make(chan *dropEvent)
	sink := &retransmitDropWriterStub{}
	source := newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{})
	done := make(chan error, 1)
	go func() {
		done <- runRetransmitWithDrop(ctx, &retransmitDropSession{
			retransmitEvents: retransmitEvents,
			dropwatchEvents:  dropwatchEvents,
			dropwatchSource:  source,
			sink:             sink,
		})
	}()

	retransmit := testRetransmitEvent(
		uint64(time.Second)+1,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	retransmitEvents <- retransmit
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
	dropwatchEvents <- drop
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("runRetransmitWithDrop() error = %v", err)
	}
	if len(sink.events) != 1 || sink.events[0] != retransmit {
		t.Fatalf("events = %+v, want pending retransmission exactly once", sink.events)
	}
	for _, reason := range []types.CorrelationReason{
		types.CorrelationReasonNoMatchingDrop,
		types.CorrelationReasonCrossNetNSCandidate,
	} {
		if !hasCorrelationReason(retransmit, reason) {
			t.Fatalf("finalized reasons = %v, want %q", retransmit.CorrelationReasons, reason)
		}
	}
}

func TestRetransmitDropWritesPendingBeforeOutputClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	retransmitEvents := make(chan *types.TCPRetransmitTracing)
	dropwatchEvents := make(chan *dropEvent)
	sink := &retransmitDropWriterStub{}
	source := newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{})
	done := make(chan error, 1)
	go func() {
		done <- runRetransmitOutputSession(func() error {
			return runRetransmitWithDrop(ctx, &retransmitDropSession{
				retransmitEvents: retransmitEvents,
				dropwatchEvents:  dropwatchEvents,
				dropwatchSource:  source,
				sink:             sink,
			})
		}, sink.close)
	}()

	retransmitEvents <- testRetransmitEvent(
		uint64(time.Second)+1,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runRetransmitOutputSession() error = %v", err)
	}
	if want := []string{"write", "output_close"}; !slices.Equal(sink.operations, want) {
		t.Fatalf("operations = %v, want %v", sink.operations, want)
	}
}

func TestRetransmitDropDeferredSettlePropagatesWriteError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writeErr := errors.New("write failed")
	retransmitEvents := make(chan *types.TCPRetransmitTracing)
	dropwatchEvents := make(chan *dropEvent)
	sink := &retransmitDropWriterStub{err: writeErr}
	source := newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{})
	done := make(chan error, 1)
	go func() {
		done <- runRetransmitWithDrop(ctx, &retransmitDropSession{
			retransmitEvents: retransmitEvents,
			dropwatchEvents:  dropwatchEvents,
			dropwatchSource:  source,
			sink:             sink,
		})
	}()

	retransmitEvents <- testRetransmitEvent(
		uint64(time.Second)+1,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	cancel()

	if err := <-done; !errors.Is(err, writeErr) {
		t.Fatalf("runRetransmitWithDrop() error = %v, want %v", err, writeErr)
	}
}

func TestRetransmitDropRejectsUnexpectedSourceClosure(t *testing.T) {
	tests := []struct {
		name        string
		closeSource func(chan *types.TCPRetransmitTracing, chan *dropEvent)
		wantError   string
	}{
		{
			name: "retransmit source",
			closeSource: func(retransmits chan *types.TCPRetransmitTracing, _ chan *dropEvent) {
				close(retransmits)
			},
			wantError: "TCP retransmit event source closed unexpectedly",
		},
		{
			name: "dropwatch source",
			closeSource: func(_ chan *types.TCPRetransmitTracing, drops chan *dropEvent) {
				close(drops)
			},
			wantError: "embedded dropwatch event source closed unexpectedly",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			retransmitEvents := make(chan *types.TCPRetransmitTracing)
			dropwatchEvents := make(chan *dropEvent)
			test.closeSource(retransmitEvents, dropwatchEvents)

			err := runRetransmitWithDrop(t.Context(), &retransmitDropSession{
				retransmitEvents: retransmitEvents,
				dropwatchEvents:  dropwatchEvents,
				dropwatchSource:  newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{}),
				sink:             &retransmitDropWriterStub{},
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("runRetransmitWithDrop() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestEmitRetransmitDropResultsPreservesErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	statusErr := errors.New("status failed")
	source := newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{})
	source.object.(*dropwatchSourceBPFStub).readErr = statusErr
	err := emitRetransmitDropResults(
		source,
		&retransmitDropWriterStub{err: writeErr},
		[]retransmitDropResult{{
			retransmit: &types.TCPRetransmitTracing{},
		}},
	)
	if !errors.Is(err, writeErr) {
		t.Fatalf("emit error = %v, want %v", err, writeErr)
	}
	if !errors.Is(err, statusErr) {
		t.Fatalf("emit error = %v, want %v", err, statusErr)
	}
	source.object.(*dropwatchSourceBPFStub).readErr = nil
	if err := emitRetransmitDropResults(
		source,
		&retransmitDropWriterStub{},
		[]retransmitDropResult{{}},
	); err == nil {
		t.Fatal("nil retransmission error = nil")
	}
}

func TestEmitRetransmitDropResultsReadsPerfStatusOncePerBatch(t *testing.T) {
	source := newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{})
	object := source.object.(*dropwatchSourceBPFStub)
	results := []retransmitDropResult{
		{
			retransmit: &types.TCPRetransmitTracing{},
			correlationReasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
			},
		},
		{
			retransmit: &types.TCPRetransmitTracing{},
			correlationReasons: []types.CorrelationReason{
				types.CorrelationReasonNoMatchingDrop,
			},
		},
	}
	if err := emitRetransmitDropResults(
		source,
		&retransmitDropWriterStub{},
		results,
	); err != nil {
		t.Fatalf("emitRetransmitDropResults() error = %v", err)
	}
	// readPerfStatus reads two maps (perf stats and rate-limit state) once
	// per batch, not once per result.
	if object.readCalls != 2 {
		t.Fatalf("perf status reads = %d, want 2", object.readCalls)
	}
}

func TestEmitMatchedRetransmitDoesNotReadPerfStatus(t *testing.T) {
	statusErr := errors.New("unexpected status read")
	source := newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{})
	object := source.object.(*dropwatchSourceBPFStub)
	object.readErr = statusErr
	result := retransmitDropResult{
		retransmit: &types.TCPRetransmitTracing{},
		drop:       &dropEvent{},
	}
	if err := emitRetransmitDropResults(
		source,
		&retransmitDropWriterStub{},
		[]retransmitDropResult{result},
	); err != nil {
		t.Fatalf("emitRetransmitDropResults() error = %v", err)
	}
	if object.readCalls != 0 {
		t.Fatalf("perf status reads = %d, want 0", object.readCalls)
	}
}

func TestEmitRetransmitDropResultsUsesLatestPerfStatus(t *testing.T) {
	correlator := newTestRetransmitDropCorrelator(t, 1)
	event := &types.TCPRetransmitTracing{
		KtimeNS: uint64(maxDropToRetransmitAge) + 1,
	}
	result := correlator.noMatchResult(event, false)
	source := newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{
		PerfLost:    2,
		LostSamples: 5,
		RateLimited: 3,
	})
	sink := &retransmitDropWriterStub{}

	if err := emitRetransmitDropResults(
		source,
		sink,
		[]retransmitDropResult{result},
	); err != nil {
		t.Fatalf("emitRetransmitDropResults() error = %v", err)
	}
	if len(sink.events) != 1 || event.DropwatchPerfStatus == nil ||
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

func TestEmitRetransmitDropResultsWritesOnceWhenPerfStatusFails(t *testing.T) {
	statusErr := errors.New("status unavailable")
	source := newTraceTestDropwatchSource(t, types.DropwatchPerfStatus{})
	source.object.(*dropwatchSourceBPFStub).readErr = statusErr
	event := &types.TCPRetransmitTracing{}
	result := retransmitDropResult{
		retransmit: event,
		correlationReasons: []types.CorrelationReason{
			types.CorrelationReasonNoMatchingDrop,
		},
	}
	sink := &retransmitDropWriterStub{}

	err := emitRetransmitDropResults(
		source,
		sink,
		[]retransmitDropResult{result},
	)
	if !errors.Is(err, statusErr) {
		t.Fatalf("emit error = %v, want %v", err, statusErr)
	}
	if len(sink.events) != 1 || sink.events[0] != event {
		t.Fatalf("events = %+v, want event exactly once", sink.events)
	}
	if event.DropwatchPerfStatus != nil || !hasCorrelationReason(
		event,
		types.CorrelationReasonDropwatchPerfStatusUnavailable,
	) {
		t.Fatalf("emitted event = %+v, want unavailable status reason", event)
	}
}

func newTraceTestDropwatchSource(
	t *testing.T,
	status types.DropwatchPerfStatus,
) *dropwatchSource {
	t.Helper()
	source := &dropwatchSource{
		object: &dropwatchSourceBPFStub{
			perfRaw: encodeDropwatchPerfStats(
				t,
				abi.BPFPerfOutputStats{ErrorCounter: status.PerfLost},
			),
			rateRaw: encodeBPFRatelimitEvent(t, status.RateLimited),
		},
		reader:            &dropwatchSourceReaderStub{},
		perfStatusMap:     testDropwatchPerfStatusMapID,
		rateLimitStateMap: testDropwatchRateLimitMapID,
	}
	source.lostSamples.Store(status.LostSamples)
	return source
}
