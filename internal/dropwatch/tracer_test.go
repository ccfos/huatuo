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

package dropwatch

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/pkg/types"
)

type setupBPFStub struct {
	tracerBPFStub
	failAt      string
	setupErr    error
	eventReader bpf.PerfEventReader
	alertReader bpf.PerfEventReader
	filterItems []bpf.MapItem
}

func (s *setupBPFStub) MapIDByName(name string) uint32 {
	if s.failAt == "maps" {
		return 0
	}
	if name == netdevFilterModeMap {
		return 9
	}
	return s.tracerBPFStub.MapIDByName(name)
}

func (s *setupBPFStub) WriteMapItems(_ uint32, items []bpf.MapItem) error {
	s.operations = append(s.operations, "filter")
	if s.failAt == "filter" {
		return s.setupErr
	}
	s.filterItems = items
	return nil
}

func (s *setupBPFStub) EventPipeByName(ctx context.Context, name string, _ uint32) (bpf.PerfEventReader, error) {
	s.operations = append(s.operations, name)
	if s.failAt == name {
		return nil, s.setupErr
	}
	reader := s.eventReader
	if name != "perf_events" {
		reader = s.alertReader
	}
	if blocking, ok := reader.(*blockingReaderStub); ok {
		blocking.ctx = ctx
	}
	return reader, nil
}

func (s *setupBPFStub) Attach() error {
	s.operations = append(s.operations, "attach")
	if s.failAt == "attach" {
		return s.setupErr
	}
	return nil
}

type blockingReaderStub struct {
	bpf.PerfEventReader
	ctx      context.Context
	started  chan struct{}
	readErr  error
	closes   atomic.Uint32
	readGate <-chan struct{}
}

func (r *blockingReaderStub) ReadInto(any) error {
	if r.readGate != nil {
		<-r.readGate
	}
	if r.started != nil {
		close(r.started)
	}
	if r.readErr != nil {
		return r.readErr
	}
	<-r.ctx.Done()
	return types.ErrExitByCancelCtx
}
func (r *blockingReaderStub) Close() error { r.closes.Add(1); return nil }

func newSetupBPF(t *testing.T) *setupBPFStub {
	t.Helper()
	return &setupBPFStub{
		tracerBPFStub: tracerBPFStub{
			perfRaw: encodeDropwatchPerfStats(t, abi.BPFPerfOutputStats{}),
			rateRaw: encodeBPFRatelimitEvent(t, 0),
		},
		eventReader: &blockingReaderStub{},
		alertReader: &blockingReaderStub{},
	}
}

func TestTracerSetupRollback(t *testing.T) {
	setupErr := errors.New("setup failed")
	closeErr := errors.New("cleanup failed")
	for _, stage := range []string{"maps", "filter", "event_bpf_rlimit_dropwatch", "perf_events", "attach"} {
		t.Run(stage, func(t *testing.T) {
			object := newSetupBPF(t)
			object.failAt, object.setupErr, object.closeErr = stage, setupErr, closeErr
			tracer, err := newTracer(t.Context(), object,
				netdevOptions{mode: netdevModeAllow, ifindexes: []uint32{1}},
				bpf.NewRateLimiter("dropwatch", 1), false)
			if tracer != nil || err == nil {
				t.Fatalf("attach = (%v, %v)", tracer, err)
			}
			if stage != "maps" && !errors.Is(err, setupErr) {
				t.Fatalf("setup error lost: %v", err)
			}
			if !errors.Is(err, closeErr) {
				t.Fatalf("cleanup error lost: %v", err)
			}
			if object.operations[len(object.operations)-1] != "object_close" {
				t.Fatal(object.operations)
			}
			wantEventCloses, wantAlertCloses := uint32(0), uint32(0)
			if stage == "attach" {
				wantEventCloses = 1
			}
			if stage == "perf_events" || stage == "attach" {
				wantAlertCloses = 1
			}
			if got := object.eventReader.(*blockingReaderStub).closes.Load(); got != wantEventCloses {
				t.Fatalf("event closes = %d, want %d", got, wantEventCloses)
			}
			if got := object.alertReader.(*blockingReaderStub).closes.Load(); got != wantAlertCloses {
				t.Fatalf("alert closes = %d, want %d", got, wantAlertCloses)
			}
		})
	}
}

func TestTracerOpenOrderAndHardwareState(t *testing.T) {
	object := newSetupBPF(t)
	tracer, err := newTracer(t.Context(), object,
		netdevOptions{mode: netdevModeAllow, ifindexes: []uint32{17}},
		bpf.NewRateLimiter("dropwatch", 1), true)
	if err != nil {
		t.Fatal(err)
	}
	if !tracer.HardwareEnabled() {
		t.Fatal("hardware disabled")
	}
	if want := []string{"filter", "event_bpf_rlimit_dropwatch", "perf_events", "attach"}; !slices.Equal(object.operations, want) {
		t.Fatalf("setup = %v, want %v", object.operations, want)
	}
	if len(object.filterItems) != 1 || binary.NativeEndian.Uint32(object.filterItems[0].Key) != 17 {
		t.Fatalf("filter items = %v", object.filterItems)
	}
	if err := tracer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := object.alertReader.(*blockingReaderStub).closes.Load(); got != 1 {
		t.Fatalf("alert closes = %d", got)
	}
}

func TestTracerIdleReadStops(t *testing.T) {
	for _, mode := range []string{"cancel", "close", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			object := newSetupBPF(t)
			reader := object.eventReader.(*blockingReaderStub)
			reader.started = make(chan struct{})
			tracer, err := newTracer(ctx, object, netdevOptions{}, bpf.NewRateLimiter("dropwatch", 0), false)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { var record abi.DropwatchPacketEvent; done <- tracer.ReadInto(&record) }()
			<-reader.started
			wantErr := types.ErrExitByCancelCtx
			switch mode {
			case "close":
				if err := tracer.Close(); err != nil {
					t.Fatal(err)
				}
			case "deadline":
				cancel(context.DeadlineExceeded)
			default:
				cancel(context.Canceled)
			}
			if err := <-done; !errors.Is(err, wantErr) {
				t.Fatalf("read = %v, want %v", err, wantErr)
			}
			if mode != "close" {
				if _, err := tracer.ReadStatus(); err != nil {
					t.Fatalf("final status: %v", err)
				}
			}
			if err := tracer.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := tracer.ReadStatus(); !errors.Is(err, bpf.ErrClosed) {
				t.Fatalf("closed status: %v", err)
			}
		})
	}
}

func TestTracerLostSamplesAndReadErrors(t *testing.T) {
	readErr := errors.New("decode failed")
	record := &abi.DropwatchPacketEvent{}
	record.Meta.KernelObservedNS = 42
	reader := &tracerReaderStub{
		errors:  []error{&bpf.PerfEventSamplesLostError{Count: 5}, readErr},
		records: []*abi.DropwatchPacketEvent{record},
	}
	object := newSetupBPF(t)
	object.eventReader = reader
	tracer, err := newTracer(t.Context(), object, netdevOptions{}, bpf.NewRateLimiter("dropwatch", 0), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tracer.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := tracer.ReadInto(nil); err == nil {
		t.Fatal("nil destination accepted")
	}
	var got abi.DropwatchPacketEvent
	if err := tracer.ReadInto(&got); !errors.Is(err, bpf.ErrPerfEventSamplesLost) {
		t.Fatal(err)
	}
	status, err := tracer.ReadStatus()
	if err != nil || status.LostSamples != 5 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if err := tracer.ReadInto(&got); !errors.Is(err, readErr) {
		t.Fatal(err)
	}
	if err := tracer.ReadInto(&got); err != nil || got.Meta.KernelObservedNS != 42 {
		t.Fatalf("record = %d, %v", got.Meta.KernelObservedNS, err)
	}
	object.readErr = readErr
	status, err = tracer.ReadStatus()
	if !errors.Is(err, readErr) || status.LostSamples != 5 {
		t.Fatalf("partial status = %+v, %v", status, err)
	}
}

func TestTracerAlertFailureInterruptsRead(t *testing.T) {
	alertErr := errors.New("alert reader failed")
	object := newSetupBPF(t)
	object.alertReader.(*blockingReaderStub).readErr = alertErr
	started := make(chan struct{})
	object.eventReader.(*blockingReaderStub).started = started
	object.alertReader.(*blockingReaderStub).readGate = started
	tracer, err := newTracer(t.Context(), object, netdevOptions{}, bpf.NewRateLimiter("dropwatch", 1), false)
	if err != nil {
		t.Fatal(err)
	}
	var record abi.DropwatchPacketEvent
	if err := tracer.ReadInto(&record); !errors.Is(err, types.ErrExitByCancelCtx) {
		t.Fatalf("read = %v", err)
	}
	if err := tracer.ReadInto(&record); !errors.Is(err, alertErr) {
		t.Fatalf("read after alert failure = %v, want %v", err, alertErr)
	}
	if err := tracer.Close(); !errors.Is(err, alertErr) {
		t.Fatalf("close = %v", err)
	}
}

func TestTracerReadIntoPreservesSuccess(t *testing.T) {
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			record := &abi.DropwatchPacketEvent{}
			record.Meta.KernelObservedNS = 42
			reader := &tracerReaderStub{records: []*abi.DropwatchPacketEvent{record}}
			object := newSetupBPF(t)
			object.eventReader = reader
			tracer, err := newTracer(ctx, object, netdevOptions{}, bpf.NewRateLimiter("dropwatch", 0), false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tracer.Close() })
			wantErr := context.Canceled
			reader.afterRead = cancel
			if mode == "close" {
				wantErr = bpf.ErrClosed
				reader.afterRead = func() {
					if err := tracer.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			var got abi.DropwatchPacketEvent
			if err := tracer.ReadInto(&got); err != nil || got.Meta.KernelObservedNS != 42 {
				t.Fatalf("read = %d, %v; want 42, nil", got.Meta.KernelObservedNS, err)
			}
			if err := tracer.ReadInto(&got); !errors.Is(err, wantErr) {
				t.Fatalf("next read = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestTracerConcurrentStatusAndClose(t *testing.T) {
	object := newSetupBPF(t)
	tracer, err := newTracer(t.Context(), object, netdevOptions{}, bpf.NewRateLimiter("dropwatch", 0), false)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 20; j++ {
				if _, err := tracer.ReadStatus(); err != nil && !errors.Is(err, bpf.ErrClosed) {
					t.Error(err)
				}
			}
		}()
	}
	if err := tracer.Close(); err != nil {
		t.Error(err)
	}
	workers.Wait()
	if got := object.eventReader.(*blockingReaderStub).closes.Load(); got != 1 {
		t.Fatalf("closes = %d", got)
	}
}

// Unlike a context-aware stub, this reader requires Close to interrupt a read.
type closeDrivenReaderStub struct {
	bpf.PerfEventReader
	started   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (r *closeDrivenReaderStub) ReadInto(any) error {
	close(r.started)
	<-r.closed
	return bpf.ErrClosed
}

func (r *closeDrivenReaderStub) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return r.closeErr
}

func TestTracerCloseInterruptsAlertBeforeWaiting(t *testing.T) {
	closeErr := errors.New("alert close failed")
	alert := &closeDrivenReaderStub{
		started:  make(chan struct{}),
		closed:   make(chan struct{}),
		closeErr: closeErr,
	}
	object := newSetupBPF(t)
	object.alertReader = alert
	tracer, err := newTracer(t.Context(), object, netdevOptions{}, bpf.NewRateLimiter("dropwatch", 1), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = alert.Close()
		_ = tracer.Close()
	})
	<-alert.started
	done := make(chan error, 1)
	go func() { done <- tracer.Close() }()
	select {
	case err := <-done:
		if !errors.Is(err, closeErr) {
			t.Fatalf("Close() = %v, want %v", err, closeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited for the alert worker before interrupting its reader")
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("repeated Close() = %v, want nil", err)
	}
}
