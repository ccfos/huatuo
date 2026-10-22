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
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/pkg/types"
)

type readerStub struct {
	bpf.PerfEventReader
	ctx      context.Context
	started  chan struct{}
	closed   chan struct{}
	readErr  error
	closeErr error
	readGate <-chan struct{}
}

func (r *readerStub) ReadInto(any) error {
	if r.readGate != nil {
		<-r.readGate
	}
	if r.started != nil {
		close(r.started)
	}
	if r.readErr != nil {
		return r.readErr
	}
	select {
	case <-r.ctx.Done():
	case <-r.closed:
	}
	return types.ErrExitByCancelCtx
}
func (r *readerStub) Close() error { close(r.closed); return r.closeErr }

type objectStub struct {
	bpf.BPF
	events     *readerStub
	alerts     *readerStub
	failAt     string
	setupErr   error
	closeErr   error
	operations []string
}

func (s *objectStub) EventPipeByName(ctx context.Context, name string, _ uint32) (bpf.PerfEventReader, error) {
	s.operations = append(s.operations, name)
	if s.failAt == name {
		return nil, s.setupErr
	}
	r := s.events
	if name != perfEventMapName {
		r = s.alerts
	}
	r.ctx = ctx
	return r, nil
}

func (s *objectStub) AttachWithOptions([]bpf.AttachOption) error {
	s.operations = append(s.operations, "attach")
	if s.failAt == "attach" {
		return s.setupErr
	}
	return nil
}
func (s *objectStub) Detach() error { s.operations = append(s.operations, "detach"); return nil }
func (s *objectStub) Close() error  { s.operations = append(s.operations, "close"); return s.closeErr }

func newObjectStub() *objectStub {
	return &objectStub{
		events: &readerStub{closed: make(chan struct{})},
		alerts: &readerStub{closed: make(chan struct{})},
	}
}

func requireClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatal("reader remains open")
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("worker did not signal")
	}
}

func TestOpenValidation(t *testing.T) {
	for _, cfg := range []*Config{nil, {}} {
		if _, err := Open(t.Context(), cfg); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Open(ctx, &Config{BPFPath: "unused.o"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open = %v, want cancellation before file access", err)
	}
}

func TestSetupRollback(t *testing.T) {
	for _, failAt := range []string{"event_bpf_rlimit_tcp_retransmit", perfEventMapName, "attach"} {
		t.Run(failAt, func(t *testing.T) {
			s := newObjectStub()
			s.failAt = failAt
			s.setupErr = errors.New("setup failed")
			s.closeErr = errors.New("close failed")
			tracer, err := newTracer(t.Context(), s, bpf.NewRateLimiter("tcp_retransmit", 1), false)
			if tracer != nil || !errors.Is(err, s.setupErr) || !errors.Is(err, s.closeErr) {
				t.Fatalf("newTracer = %v, %v", tracer, err)
			}
			if !slices.Equal(s.operations[len(s.operations)-2:], []string{"detach", "close"}) {
				t.Fatalf("cleanup = %v", s.operations)
			}
			if failAt != "event_bpf_rlimit_tcp_retransmit" {
				requireClosed(t, s.alerts.closed)
			}
			if failAt == "attach" {
				requireClosed(t, s.events.closed)
			}
		})
	}
}

func TestCloseInterruptsRead(t *testing.T) {
	s := newObjectStub()
	s.events.started = make(chan struct{})
	tracer, err := newTracer(t.Context(), s, bpf.NewRateLimiter("tcp_retransmit", 1), false)
	if err != nil {
		t.Fatal(err)
	}
	defer tracer.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var record abi.TCPRetransmitEvent
		if err := tracer.ReadInto(&record); !errors.Is(err, types.ErrExitByCancelCtx) {
			t.Errorf("read = %v", err)
		}
	}()
	waitSignal(t, s.events.started)
	if err := tracer.Close(); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, done)
	requireClosed(t, s.events.closed)
	requireClosed(t, s.alerts.closed)
	if err := tracer.Close(); err != nil {
		t.Fatal(err)
	}
	var record abi.TCPRetransmitEvent
	if err := tracer.ReadInto(&record); !errors.Is(err, bpf.ErrClosed) {
		t.Fatalf("read after close = %v", err)
	}
}

func TestAlertFailureStopsReading(t *testing.T) {
	s := newObjectStub()
	alertErr := errors.New("alert failed")
	s.alerts.readErr = alertErr
	tracer, err := newTracer(t.Context(), s, bpf.NewRateLimiter("tcp_retransmit", 1), false)
	if err != nil {
		t.Fatal(err)
	}
	defer tracer.Close()
	waitSignal(t, tracer.ctx.Done())
	var record abi.TCPRetransmitEvent
	if err := tracer.ReadInto(&record); !errors.Is(err, alertErr) {
		t.Fatalf("read = %v", err)
	}
	if err := tracer.Close(); !errors.Is(err, alertErr) {
		t.Fatalf("close = %v", err)
	}
}

func TestAlertFailureInterruptsRead(t *testing.T) {
	s := newObjectStub()
	s.events.started = make(chan struct{})
	s.alerts.readGate = s.events.started
	alertErr := errors.New("alert failed during event read")
	s.alerts.readErr = alertErr
	tracer, err := newTracer(t.Context(), s, bpf.NewRateLimiter("tcp_retransmit", 1), false)
	if err != nil {
		t.Fatal(err)
	}
	defer tracer.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var record abi.TCPRetransmitEvent
		if err := tracer.ReadInto(&record); !errors.Is(err, types.ErrExitByCancelCtx) {
			t.Errorf("read = %v, want underlying cancellation", err)
		}
	}()
	waitSignal(t, done)
	if err := tracer.Close(); !errors.Is(err, alertErr) {
		t.Fatalf("close = %v, want alert failure", err)
	}
}

func TestParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s := newObjectStub()
	tracer, err := newTracer(ctx, s, bpf.NewRateLimiter("tcp_retransmit", 0), false)
	if err != nil {
		t.Fatal(err)
	}
	defer tracer.Close()
	cancel()
	var record abi.TCPRetransmitEvent
	if err := tracer.ReadInto(&record); !errors.Is(err, context.Canceled) {
		t.Fatalf("read = %v", err)
	}
	select {
	case <-s.events.closed:
		t.Fatal("cancellation released resources before Close")
	default:
	}
	if err := tracer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadIntoPreservesResult(t *testing.T) {
	for _, readErr := range []error{nil, &bpf.PerfEventSamplesLostError{Count: 2}, types.ErrExitByCancelCtx} {
		ctx, cancel := context.WithCancelCause(t.Context())
		tracer := &Tracer{ctx: ctx}
		tracer.reader = &resultReader{read: func(dst any) error {
			dst.(*abi.TCPRetransmitEvent).KernelObservedNS = 42
			cancel(context.Canceled)
			return readErr
		}}
		var record abi.TCPRetransmitEvent
		if err := tracer.ReadInto(&record); !errors.Is(err, readErr) {
			t.Fatalf("read = %v, want %v", err, readErr)
		}
		if record.KernelObservedNS != 42 {
			t.Fatal("read result overwritten")
		}
	}
}

type resultReader struct {
	bpf.PerfEventReader
	read func(any) error
}

func (r *resultReader) ReadInto(dst any) error { return r.read(dst) }

func BenchmarkReadInto(b *testing.B) {
	reader := &resultReader{read: func(dst any) error {
		dst.(*abi.TCPRetransmitEvent).KernelObservedNS = 42
		return nil
	}}
	tracer := &Tracer{ctx: b.Context(), reader: reader}
	for _, name := range []string{"reader", "tracer"} {
		b.Run(name, func(b *testing.B) {
			var record abi.TCPRetransmitEvent
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if name == "reader" {
					err = reader.ReadInto(&record)
				} else {
					err = tracer.ReadInto(&record)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
