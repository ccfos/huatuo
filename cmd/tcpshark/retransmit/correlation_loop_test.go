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
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

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

func TestRetransmitDropTimerRearmsAfterMatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	retransmits := make(chan *retransmitEvent)
	drops := make(chan *dropEvent)
	outputs := make(chan *types.TCPRetransmitTracing)
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return runRetransmitDropCorrelation(groupCtx, &retransmitDropSession{
			retransmitEvents:    retransmits,
			dropwatchEvents:     drops,
			readDropwatchStatus: source.ReadStatus,
			sink: testEventWriter(func(event *types.TCPRetransmitTracing) error {
				select {
				case outputs <- event:
				case <-groupCtx.Done():
				}
				return nil
			}),
		})
	})
	t.Cleanup(func() {
		cancel()
		if err := group.Wait(); err != nil {
			t.Errorf("correlation loop: %v", err)
		}
	})

	first := testRetransmitEvent(uint64(time.Second)+1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	select {
	case retransmits <- first:
	case <-ctx.Done():
		t.Fatal("correlation loop did not accept retransmission")
	}
	drop := testDropEvent(t, uint64(time.Second), "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK)
	select {
	case drops <- drop:
	case <-ctx.Done():
		t.Fatal("correlation loop did not accept drop")
	}
	select {
	case event := <-outputs:
		if event.CorrelationReason != types.CorrelationMatched {
			t.Fatal("first retransmission did not match drop")
		}
	case <-ctx.Done():
		t.Fatal("matched retransmission was not emitted")
	}

	// Leave the queue empty beyond the old deadline before rearming the timer.
	select {
	case event := <-outputs:
		t.Fatalf("unexpected output with empty queue: %+v", event)
	case <-time.After(2 * retransmitRetentionDuration):
	}
	second := testRetransmitEvent(uint64(2*time.Second), "10.0.0.1", "10.0.0.2", 1001, 80, 100, 200)
	start := time.Now()
	select {
	case retransmits <- second:
	case <-ctx.Done():
		t.Fatal("correlation loop did not accept second retransmission")
	}
	select {
	case event := <-outputs:
		if event.KernelObservedNS != second.record.KernelObservedNS ||
			event.CorrelationReason != types.CorrelationWaitTimeout {
			t.Fatalf("timeout output = %+v, want unmatched second retransmission", event)
		}
		if elapsed := time.Since(start); elapsed < retransmitRetentionDuration {
			t.Fatalf("retransmission expired after %s, before its deadline", elapsed)
		}
	case <-ctx.Done():
		t.Fatal("pending retransmission did not expire without further input")
	}
}

func TestRetransmitDropStatusErrorStopsAfterMatchedOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	retransmits := make(chan *retransmitEvent)
	drops := make(chan *dropEvent)
	statusErr := errors.New("status unavailable")
	source := &dropwatchStatusStub{readErr: statusErr}
	sink := &retransmitDropWriterStub{}
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		return runRetransmitDropCorrelation(groupCtx, &retransmitDropSession{
			retransmitEvents:    retransmits,
			dropwatchEvents:     drops,
			readDropwatchStatus: source.ReadStatus,
			sink:                sink,
		})
	})
	t.Cleanup(func() {
		cancel()
		_ = group.Wait()
	})

	drop := testDropEvent(t, uint64(time.Second), "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200, 0, packet.TCPFlagACK)
	select {
	case drops <- drop:
	case <-groupCtx.Done():
		t.Fatal("correlation loop did not accept drop")
	}
	retransmit := testRetransmitEvent(uint64(time.Second)+1, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
	select {
	case retransmits <- retransmit:
	case <-groupCtx.Done():
		t.Fatal("correlation loop stopped before accepting retransmission")
	}

	if err := group.Wait(); !errors.Is(err, statusErr) {
		t.Fatalf("correlation loop error = %v, want %v", err, statusErr)
	}
	if len(sink.events) != 1 || sink.events[0].KernelObservedNS != retransmit.record.KernelObservedNS ||
		sink.events[0].DropLocation != "software" || sink.events[0].CorrelationReason != types.CorrelationMatched {
		t.Fatalf("events = %+v, want matched retransmission exactly once", sink.events)
	}
	if source.readCalls != 1 {
		t.Fatalf("status reads = %d, want 1", source.readCalls)
	}
}

func TestRetransmitReaderErrorCancelsSiblingWorkers(t *testing.T) {
	group, groupCtx := errgroup.WithContext(t.Context())
	sourceErr := errors.New("source failed")
	reader := &retransmitReaderStub{read: func(any) error {
		return sourceErr
	}}
	events := make(chan *retransmitEvent)
	group.Go(func() error {
		defer close(events)
		return readRetransmitEvents(groupCtx, reader.readInto, events)
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

func TestRetransmitReaderCancelsPendingSend(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	group, groupCtx := errgroup.WithContext(ctx)
	read := make(chan struct{})
	reader := &retransmitReaderStub{read: func(any) error {
		close(read)
		return nil
	}}
	events := make(chan *retransmitEvent)
	group.Go(func() error {
		defer close(events)
		return readRetransmitEvents(groupCtx, reader.readInto, events)
	})
	<-read
	// No receiver is available; cancellation must allow the sender to exit.
	cancel()
	done := make(chan error, 1)
	go func() { done <- group.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not stop after cancellation")
	}
	if _, open := <-events; open {
		t.Fatal("reader left event channel open")
	}
}

func TestRetransmitDropCancellationFinalizesPendingEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	retransmitEvents := make(chan *retransmitEvent)
	dropwatchEvents := make(chan *dropEvent)
	sink := &retransmitDropWriterStub{}
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	done := make(chan error, 1)
	go func() {
		done <- runRetransmitDropCorrelation(ctx, &retransmitDropSession{
			retransmitEvents:    retransmitEvents,
			dropwatchEvents:     dropwatchEvents,
			readDropwatchStatus: source.ReadStatus,
			sink:                sink,
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
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("runRetransmitDropCorrelation() error = %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].KernelObservedNS != retransmit.record.KernelObservedNS {
		t.Fatalf("events = %+v, want pending retransmission exactly once", sink.events)
	}
	if sink.events[0].DropLocation != "unknown" ||
		sink.events[0].CorrelationReason != types.CorrelationInterrupted ||
		sink.events[0].DropPerfStatus == nil || sink.events[0].DropStack != "" {
		t.Fatalf("finalized retransmission = %+v, want unknown no-match result", retransmit)
	}
}

func TestRetransmitDropCancellationRetainsNetNamespace(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	retransmitEvents := make(chan *retransmitEvent)
	dropwatchEvents := make(chan *dropEvent)
	sink := &retransmitDropWriterStub{}
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	done := make(chan error, 1)
	go func() {
		done <- runRetransmitDropCorrelation(ctx, &retransmitDropSession{
			retransmitEvents:    retransmitEvents,
			dropwatchEvents:     dropwatchEvents,
			readDropwatchStatus: source.ReadStatus,
			sink:                sink,
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
		300,
		400,
		0,
		packet.TCPFlagACK,
	)
	dropwatchEvents <- drop
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("runRetransmitDropCorrelation() error = %v", err)
	}
	if len(sink.events) != 1 || sink.events[0].KernelObservedNS != retransmit.record.KernelObservedNS {
		t.Fatalf("events = %+v, want pending retransmission exactly once", sink.events)
	}
	if sink.events[0].CorrelationReason != types.CorrelationInterrupted || !sink.events[0].NetNamespace {
		t.Fatalf("finalized event = %+v, want interruption with a namespace match", sink.events[0])
	}
}

func TestRetransmitDropWritesPendingBeforeOutputClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	retransmitEvents := make(chan *retransmitEvent)
	dropwatchEvents := make(chan *dropEvent)
	sink := &retransmitDropWriterStub{}
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	done := make(chan error, 1)
	go func() {
		done <- runRetransmitDropCorrelation(ctx, &retransmitDropSession{
			retransmitEvents:    retransmitEvents,
			dropwatchEvents:     dropwatchEvents,
			readDropwatchStatus: source.ReadStatus,
			sink:                sink,
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
	if err := <-done; err != nil {
		t.Fatalf("runRetransmitDropCorrelation() error = %v", err)
	}
	if err := sink.close(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"write", "output_close"}; !slices.Equal(sink.operations, want) {
		t.Fatalf("operations = %v, want %v", sink.operations, want)
	}
}

func TestRetransmitDropDeferredSettlePropagatesWriteError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writeErr := errors.New("write failed")
	retransmitEvents := make(chan *retransmitEvent)
	dropwatchEvents := make(chan *dropEvent)
	sink := &retransmitDropWriterStub{err: writeErr}
	source := newTraceTestDropwatchStatus(t, types.DropwatchStatus{})
	done := make(chan error, 1)
	go func() {
		done <- runRetransmitDropCorrelation(ctx, &retransmitDropSession{
			retransmitEvents:    retransmitEvents,
			dropwatchEvents:     dropwatchEvents,
			readDropwatchStatus: source.ReadStatus,
			sink:                sink,
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
		t.Fatalf("runRetransmitDropCorrelation() error = %v, want %v", err, writeErr)
	}
}

func TestRetransmitDropRejectsUnexpectedSourceClosure(t *testing.T) {
	tests := []struct {
		name        string
		closeSource func(chan *retransmitEvent, chan *dropEvent)
		wantError   string
	}{
		{
			name: "retransmit source",
			closeSource: func(retransmits chan *retransmitEvent, _ chan *dropEvent) {
				close(retransmits)
			},
			wantError: "TCP retransmit event source closed unexpectedly",
		},
		{
			name: "dropwatch source",
			closeSource: func(_ chan *retransmitEvent, drops chan *dropEvent) {
				close(drops)
			},
			wantError: "embedded dropwatch event source closed unexpectedly",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			retransmitEvents := make(chan *retransmitEvent)
			dropwatchEvents := make(chan *dropEvent)
			test.closeSource(retransmitEvents, dropwatchEvents)

			err := runRetransmitDropCorrelation(t.Context(), &retransmitDropSession{
				retransmitEvents:    retransmitEvents,
				dropwatchEvents:     dropwatchEvents,
				readDropwatchStatus: newTraceTestDropwatchStatus(t, types.DropwatchStatus{}).ReadStatus,
				sink:                &retransmitDropWriterStub{},
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("runRetransmitDropCorrelation() error = %v, want %q", err, test.wantError)
			}
		})
	}
}
