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
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/pkg/types"
)

type retransmitReaderStub struct {
	bpf.PerfEventReader
	readCalls int
	read      func(any) error
}

func (s *retransmitReaderStub) ReadInto(destination any) error {
	s.readCalls++
	return s.read(destination)
}

func TestWriteRetransmitEventsRetriesLostSamples(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reader := &retransmitReaderStub{}
	reader.read = func(destination any) error {
		if reader.readCalls == 1 {
			return &bpf.PerfEventSamplesLostError{Count: 2}
		}
		record, ok := destination.(*abi.TCPRetransmitEvent)
		if !ok {
			return errors.New("unexpected record type")
		}
		record.KernelObservedNS = 42
		return nil
	}

	var consumed []uint64
	err := writeRetransmitEvents(ctx, reader.readInto, testEventWriter(func(record *types.TCPRetransmitTracing) error {
		consumed = append(consumed, record.KernelObservedNS)
		cancel()
		return nil
	}), "tools")
	if err != nil {
		t.Fatalf("writeRetransmitEvents() error = %v", err)
	}
	if reader.readCalls != 2 {
		t.Fatalf("ReadInto() calls = %d, want 2", reader.readCalls)
	}
	if len(consumed) != 1 || consumed[0] != 42 {
		t.Fatalf("consumed records = %v, want [42]", consumed)
	}
}

func TestWriteRetransmitEventsStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	readErr := errors.New("reader closed")
	reader := &retransmitReaderStub{read: func(any) error {
		cancel()
		return readErr
	}}

	err := writeRetransmitEvents(ctx, reader.readInto, testEventWriter(func(*types.TCPRetransmitTracing) error {
		t.Fatal("consume called after context cancellation")
		return nil
	}), "tools")
	if err != nil {
		t.Fatalf("writeRetransmitEvents() error = %v, want nil", err)
	}
	if reader.readCalls != 1 {
		t.Fatalf("ReadInto() calls = %d, want 1", reader.readCalls)
	}
}

func TestWriteRetransmitEventsReturnsNamedReadError(t *testing.T) {
	readErr := errors.New("read failed")
	reader := &retransmitReaderStub{read: func(any) error {
		return readErr
	}}

	err := writeRetransmitEvents(t.Context(), reader.readInto, testEventWriter(func(*types.TCPRetransmitTracing) error {
		t.Fatal("consume called after read failure")
		return nil
	}), "tools")
	if !errors.Is(err, readErr) {
		t.Fatalf("writeRetransmitEvents() error = %v, want %v", err, readErr)
	}
	if !strings.Contains(err.Error(), "read TCP retransmit event") {
		t.Fatalf("writeRetransmitEvents() error = %v, want event name", err)
	}
}

func TestWriteRetransmitEventsReturnsOutputError(t *testing.T) {
	consumeErr := errors.New("write failed")
	reader := &retransmitReaderStub{read: func(any) error { return nil }}
	err := writeRetransmitEvents(t.Context(), reader.readInto, testEventWriter(func(*types.TCPRetransmitTracing) error {
		return consumeErr
	}), "tools")
	if !errors.Is(err, consumeErr) || reader.readCalls != 1 {
		t.Fatalf("read = %v after %d calls; want consumer error after one read", err, reader.readCalls)
	}
}

// The batch includes one terminal writer error and its operation wrapper.
func BenchmarkWriteRetransmitEvents(b *testing.B) {
	stop := errors.New("batch complete")
	reader := &retransmitReaderStub{read: func(any) error { return nil }}
	var formatted *types.TCPRetransmitTracing
	consume := testEventWriter(func(event *types.TCPRetransmitTracing) error {
		formatted = event
		if reader.readCalls == 64 {
			return stop
		}
		return nil
	})
	b.ReportAllocs()
	for b.Loop() {
		reader.readCalls = 0
		if err := writeRetransmitEvents(b.Context(), reader.readInto, consume, "tools"); !errors.Is(err, stop) {
			b.Fatal(err)
		}
	}
	_ = formatted
}

func (s *retransmitReaderStub) readInto(dst *abi.TCPRetransmitEvent) error {
	return s.ReadInto(dst)
}

func TestRetransmitReaderOwnsDeliveredEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	group, ctx := errgroup.WithContext(ctx)
	var calls uint64
	before := time.Now()
	events := make(chan *retransmitEvent)
	group.Go(func() error {
		defer close(events)
		return readRetransmitEvents(ctx, func(record *abi.TCPRetransmitEvent) error {
			calls++
			if calls > 2 {
				cancel()
				return context.Canceled
			}
			record.KernelObservedNS = calls
			record.Comm[0] = byte(calls)
			record.Saddr[0] = byte(calls)
			return nil
		}, events)
	})
	var received []*retransmitEvent
	for event := range events {
		received = append(received, event)
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 {
		t.Fatalf("received %d events, want 2", len(received))
	}
	for i, event := range received {
		want := uint64(i + 1)
		if event.record.KernelObservedNS != want || event.record.Comm[0] != byte(want) ||
			event.record.Saddr[0] != byte(want) {
			t.Fatalf("event %d was overwritten: %+v", i, event)
		}
		if event.observedAt.Before(before) || event.observedAt.After(time.Now()) {
			t.Fatalf("capture time outside read interval: %v", event.observedAt)
		}
	}
}

func TestWriteRetransmitEventsPreservesSuccessfulReadAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var consumed bool
	err := writeRetransmitEvents(ctx, func(record *abi.TCPRetransmitEvent) error {
		record.KernelObservedNS = 42
		cancel()
		return nil
	}, testEventWriter(func(event *types.TCPRetransmitTracing) error {
		consumed = event.KernelObservedNS == 42
		return nil
	}), "tools")
	if err != nil || !consumed {
		t.Fatalf("read = %v, consumed = %v; want successful event", err, consumed)
	}
}

func TestDropwatchReadEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	record := newIPv4DropwatchTCPRecord(40)
	record.Meta.KernelObservedNS = 10
	record.Meta.NetNamespaceCookie = 1
	calls := 0
	read := func(dst *abi.DropwatchPacketEvent) error {
		calls++
		if calls == 1 {
			return &bpf.PerfEventSamplesLostError{Count: 2}
		}
		if calls == 2 {
			*dst = *record
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}
	events := make(chan *dropEvent)
	done := make(chan error, 1)
	go func() { done <- readDropwatchEvents(ctx, read, events) }()
	event := <-events
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if event.kernelObservedNS != 10 || event.flow != testFlowKey(12345, 80) || event.sequence != 123 {
		t.Fatalf("event = %+v", event)
	}
}

func TestDropwatchReadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	read := func(*abi.DropwatchPacketEvent) error {
		t.Fatal("read called after cancellation")
		return errors.New("unexpected read")
	}
	if err := readDropwatchEvents(ctx, read, make(chan *dropEvent)); err != nil {
		t.Fatal(err)
	}
}

func TestDropwatchReadError(t *testing.T) {
	readErr := errors.New("reader failed")
	read := func(*abi.DropwatchPacketEvent) error { return readErr }
	if err := readDropwatchEvents(t.Context(), read, make(chan *dropEvent)); !errors.Is(err, readErr) {
		t.Fatalf("readDropwatchEvents() = %v, want %v", err, readErr)
	}
}

func TestReadRetransmitEventPreservesSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var event retransmitEvent
	before := time.Now()
	err := readRetransmitEvent(ctx, func(dst *abi.TCPRetransmitEvent) error {
		dst.KernelObservedNS = 42
		cancel()
		return nil
	}, &event)
	if err != nil || event.record.KernelObservedNS != 42 ||
		event.observedAt.Before(before) || event.observedAt.After(time.Now()) {
		t.Fatalf("read = %+v, %v; want captured successful event", event, err)
	}
}

func TestReadRetransmitEventSkipsCanceledRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var event retransmitEvent
	err := readRetransmitEvent(ctx, func(*abi.TCPRetransmitEvent) error {
		t.Fatal("reader called after cancellation")
		return nil
	}, &event)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read error = %v, want cancellation", err)
	}
}

type testEventWriter func(*types.TCPRetransmitTracing) error

func (w testEventWriter) Write(event *types.TCPRetransmitTracing) error { return w(event) }
