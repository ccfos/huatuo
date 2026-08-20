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
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/netcorrelate"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/pkg/types"
)

type correlationWriter struct {
	err    error
	events []*types.TCPRetransmitTracing
}

type shutdownOperationRecorder struct {
	mu         sync.Mutex
	events     []*types.TCPRetransmitTracing
	operations []string
}

func (r *shutdownOperationRecorder) Write(event *types.TCPRetransmitTracing) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	r.operations = append(r.operations, "write")
	return nil
}

func (r *shutdownOperationRecorder) closeOutput() error {
	r.record("end")
	return nil
}

func (r *shutdownOperationRecorder) record(operation string) {
	r.mu.Lock()
	r.operations = append(r.operations, operation)
	r.mu.Unlock()
}

func (r *shutdownOperationRecorder) snapshot() (
	[]*types.TCPRetransmitTracing,
	[]string,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events), slices.Clone(r.operations)
}

func (w *correlationWriter) Write(event *types.TCPRetransmitTracing) error {
	if w.err != nil {
		return w.err
	}
	w.events = append(w.events, event)
	return nil
}

type closeTrackingReader struct {
	bpf.PerfEventReader
	closes     int
	closeErr   error
	closed     chan struct{}
	operations *[]string
}

func (r *closeTrackingReader) Close() error {
	r.closes++
	if r.operations != nil {
		*r.operations = append(*r.operations, "reader")
	}
	if r.closed != nil && r.closes == 1 {
		close(r.closed)
	}
	return r.closeErr
}

type closeTrackingBPF struct {
	bpf.BPF
	detaches   int
	closes     int
	detachErr  error
	closeErr   error
	operations []string
}

func (b *closeTrackingBPF) Detach() error {
	b.detaches++
	b.operations = append(b.operations, "detach")
	return b.detachErr
}

func (b *closeTrackingBPF) Close() error {
	b.closes++
	b.operations = append(b.operations, "close")
	return b.closeErr
}

type finalDrainBPF struct {
	closeTrackingBPF
	activeEpoch      uint32
	holdDrain        <-chan struct{}
	drainStarted     chan struct{}
	drainStartedOnce sync.Once
}

func (b *finalDrainBPF) MapIDByName(name string) uint32 {
	switch name {
	case "dropwatch_active_epoch":
		return 1
	case "dropwatch_epoch_stats":
		return 2
	default:
		return 0
	}
}

func (b *finalDrainBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	switch mapID {
	case 1:
		value := make([]byte, 4)
		binary.NativeEndian.PutUint32(value, b.activeEpoch)
		return value, nil
	case 2:
		value := make([]byte, abi.DropwatchPerfEpochStatsSize)
		if len(key) < 4 || binary.NativeEndian.Uint32(key) != 0 ||
			b.activeEpoch != 1 || b.holdDrain == nil {
			return value, nil
		}
		select {
		case <-b.holdDrain:
			return value, nil
		default:
			b.drainStartedOnce.Do(func() {
				if b.drainStarted != nil {
					close(b.drainStarted)
				}
			})
			binary.NativeEndian.PutUint64(value, 1)
			return value, nil
		}
	default:
		return nil, errors.New("unknown fake map")
	}
}

func (b *finalDrainBPF) WriteMapItems(mapID uint32, items []bpf.MapItem) error {
	if mapID != 1 || len(items) != 1 || len(items[0].Value) != 4 {
		return errors.New("invalid fake map write")
	}
	b.activeEpoch = binary.NativeEndian.Uint32(items[0].Value)
	return nil
}

type finalDrainReader struct {
	closeTrackingReader
	isFlushed bool
	flushes   int
}

func (r *finalDrainReader) PollInto(any, time.Duration) (bool, error) {
	if r.isFlushed {
		return false, bpf.ErrPerfFlushed
	}
	return false, nil
}

func (r *finalDrainReader) Flush() error {
	r.isFlushed = true
	r.flushes++
	return nil
}

type pollErrorReader struct {
	bpf.PerfEventReader
	err error
}

func (r *pollErrorReader) PollInto(any, time.Duration) (bool, error) {
	return false, r.err
}

func TestStartLocalDropSourceCancelsRunOnError(t *testing.T) {
	readErr := errors.New("read failed")
	runCanceled := make(chan struct{})
	correlation := &localCorrelation{
		source: &localDropSource{reader: &pollErrorReader{err: readErr}},
	}
	done := startLocalDropSource(
		t.Context(),
		func() { close(runCanceled) },
		make(chan context.Context),
		correlation,
	)

	select {
	case <-runCanceled:
	case <-time.After(time.Second):
		t.Fatal("source failure did not cancel the parent run")
	}
	if err := <-done; !errors.Is(err, readErr) {
		t.Fatalf("source error = %v, want %v", err, readErr)
	}
}

func TestLocalDropSourceClosePreservesErrors(t *testing.T) {
	readerErr := errors.New("reader close failed")
	detachErr := errors.New("detach failed")
	objectErr := errors.New("object close failed")
	obj := &closeTrackingBPF{detachErr: detachErr, closeErr: objectErr}
	reader := &closeTrackingReader{
		closeErr:   readerErr,
		operations: &obj.operations,
	}
	source := &localDropSource{obj: obj, reader: reader}

	err := source.close()
	for _, want := range []error{readerErr, detachErr, objectErr} {
		if !errors.Is(err, want) {
			t.Fatalf("close() error = %v, want %v", err, want)
		}
	}
	if reader.closes != 1 || obj.detaches != 1 || obj.closes != 1 {
		t.Fatalf(
			"cleanup counts: detach=%d object_close=%d reader_close=%d",
			obj.detaches,
			obj.closes,
			reader.closes,
		)
	}
	if want := []string{"detach", "reader", "close"}; !slices.Equal(obj.operations, want) {
		t.Fatalf("cleanup operations = %v, want %v", obj.operations, want)
	}
	if err := source.closeReader(); err != nil {
		t.Fatalf("second closeReader() error = %v", err)
	}
	if reader.closes != 1 {
		t.Fatalf("reader close count = %d, want 1", reader.closes)
	}
}

func TestEmitCorrelationResultsWritesEveryRetransmission(t *testing.T) {
	t.Parallel()

	sink := &correlationWriter{}
	correlation := &localCorrelation{sink: sink}
	results := []netcorrelate.CorrelationResult{
		{Retrans: &types.TCPRetransmitTracing{KtimeNS: 1}},
		{Retrans: &types.TCPRetransmitTracing{KtimeNS: 2}},
	}
	if err := emitCorrelationResults(correlation, results); err != nil {
		t.Fatalf("emitCorrelationResults() error = %v", err)
	}
	if len(sink.events) != len(results) {
		t.Fatalf("written events = %d, want %d", len(sink.events), len(results))
	}
	for i := range results {
		if sink.events[i] != results[i].Retrans {
			t.Fatalf("written event %d does not preserve result ownership", i)
		}
	}
}

func TestEmitCorrelationResultsRejectsInvalidResult(t *testing.T) {
	t.Parallel()

	err := emitCorrelationResults(
		&localCorrelation{sink: &correlationWriter{}},
		[]netcorrelate.CorrelationResult{{}},
	)
	if err == nil {
		t.Fatal("emitCorrelationResults() error = nil")
	}
}

func TestEmitCorrelationResultsPropagatesWriterError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	err := emitCorrelationResults(
		&localCorrelation{sink: &correlationWriter{err: boom}},
		[]netcorrelate.CorrelationResult{{
			Retrans: &types.TCPRetransmitTracing{},
		}},
	)
	if !errors.Is(err, boom) {
		t.Fatalf("emitCorrelationResults() error = %v, want %v", err, boom)
	}
}

func TestLocalCorrelationFinalDrainPrecedesOutputClose(t *testing.T) {
	correlator, err := netcorrelate.NewCorrelator(1)
	if err != nil {
		t.Fatalf("NewCorrelator() error = %v", err)
	}
	addPendingRetransmission(t, correlator)

	releaseDrain := make(chan struct{})
	drainStarted := make(chan struct{})
	reader := &finalDrainReader{}
	obj := &finalDrainBPF{
		holdDrain:    releaseDrain,
		drainStarted: drainStarted,
	}
	barrier, err := netcorrelate.NewPerfBarrier(obj)
	if err != nil {
		t.Fatalf("NewPerfBarrier() error = %v", err)
	}
	recorder := &shutdownOperationRecorder{}
	sourceCtx, cancelSource := context.WithCancel(context.WithoutCancel(t.Context()))
	stop := make(chan context.Context, 1)
	correlation := &localCorrelation{
		correlator:   correlator,
		source:       &localDropSource{obj: obj, reader: reader, barrier: barrier},
		sink:         recorder,
		cancelSource: cancelSource,
		sourceStop:   stop,
	}
	if events, operations := recorder.snapshot(); len(events) != 0 || len(operations) != 0 {
		t.Fatalf(
			"before shutdown: events=%d operations=%v, want no output",
			len(events),
			operations,
		)
	}
	correlation.sourceDone = startLocalDropSource(
		sourceCtx,
		func() {},
		stop,
		correlation,
	)
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- runRetransmitOutputSession(
			func() error { return correlation.close(t.Context()) },
			recorder.closeOutput,
		)
	}()
	select {
	case <-drainStarted:
	case <-time.After(time.Second):
		t.Fatal("final drain did not reach the held perf frontier")
	}
	if events, operations := recorder.snapshot(); len(events) != 0 || len(operations) != 0 {
		t.Fatalf(
			"held final drain: events=%d operations=%v, want no output",
			len(events),
			operations,
		)
	}
	close(releaseDrain)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
	recorder.record("storage_close")
	events, operations := recorder.snapshot()
	if err := validateFinalDrainShutdown(events, operations); err != nil {
		t.Fatal(err)
	}
	if obj.detaches != 1 || obj.closes != 1 || reader.closes != 1 {
		t.Fatalf(
			"cleanup counts: detach=%d object_close=%d reader_close=%d",
			obj.detaches,
			obj.closes,
			reader.closes,
		)
	}
	if reader.flushes != 1 {
		t.Fatalf("reader flush count = %d, want 1", reader.flushes)
	}
}

func TestFinalDrainShutdownContractRejectsSuppressedDrain(t *testing.T) {
	correlator, err := netcorrelate.NewCorrelator(1)
	if err != nil {
		t.Fatalf("NewCorrelator() error = %v", err)
	}
	addPendingRetransmission(t, correlator)

	sourceDone := make(chan error, 1)
	sourceDone <- nil
	recorder := &shutdownOperationRecorder{}
	correlation := &localCorrelation{
		correlator:   correlator,
		source:       &localDropSource{obj: &closeTrackingBPF{}, reader: &closeTrackingReader{}},
		sink:         recorder,
		cancelSource: func() {},
		sourceStop:   make(chan context.Context, 1),
		sourceDone:   sourceDone,
	}
	if err := runRetransmitOutputSession(
		func() error { return correlation.close(t.Context()) },
		recorder.closeOutput,
	); err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
	recorder.record("storage_close")
	events, operations := recorder.snapshot()
	if err := validateFinalDrainShutdown(events, operations); err == nil {
		t.Fatal("suppressed final drain satisfied the shutdown contract")
	}
}

func addPendingRetransmission(t *testing.T, correlator *netcorrelate.Correlator) {
	t.Helper()
	results, err := correlator.AddRetrans(&types.TCPRetransmitTracing{
		EventType:          "tcp_retransmit_skb",
		KtimeNS:            20,
		AddressFamily:      unix.AF_INET,
		TCPSaddr:           "10.0.0.1",
		TCPDaddr:           "10.0.0.2",
		TCPSport:           1000,
		TCPDport:           80,
		TCPSeq:             100,
		TCPEndSeq:          200,
		TCPFlagsRaw:        packet.TCPFlagACK,
		NetNamespaceCookie: 1,
	})
	if err != nil || len(results) != 0 {
		t.Fatalf("AddRetrans() = (%v, %v), want pending", results, err)
	}
}

func validateFinalDrainShutdown(
	events []*types.TCPRetransmitTracing,
	operations []string,
) error {
	if len(events) != 1 {
		return fmt.Errorf("shutdown events = %d, want exactly one", len(events))
	}
	event := events[0]
	if event.DropwatchPerfStatus == nil ||
		event.DropwatchPerfStatus.DrainedThroughKtimeNS < event.KtimeNS ||
		slices.Contains(
			event.CorrelationReasons,
			types.CorrelationReasonPerfFrontierIncomplete,
		) {
		return fmt.Errorf("event was not finalized by a completed perf drain: %+v", event)
	}
	if want := []string{"write", "end", "storage_close"}; !slices.Equal(operations, want) {
		return fmt.Errorf("shutdown operations = %v, want %v", operations, want)
	}
	return nil
}

func TestLocalCorrelationDeadlineClosesReaderBeforeSource(t *testing.T) {
	correlator, err := netcorrelate.NewCorrelator(1)
	if err != nil {
		t.Fatalf("NewCorrelator() error = %v", err)
	}
	readerClosed := make(chan struct{})
	readerErr := errors.New("reader close failed")
	detachErr := errors.New("detach failed")
	objectErr := errors.New("object close failed")
	obj := &closeTrackingBPF{detachErr: detachErr, closeErr: objectErr}
	reader := &closeTrackingReader{
		closed:     readerClosed,
		closeErr:   readerErr,
		operations: &obj.operations,
	}
	stop := make(chan context.Context, 1)
	sourceDone := make(chan error, 1)
	correlation := &localCorrelation{
		correlator:   correlator,
		source:       &localDropSource{obj: obj, reader: reader},
		sink:         &correlationWriter{},
		cancelSource: func() {},
		sourceStop:   stop,
		sourceDone:   sourceDone,
	}

	go func() {
		<-stop
		<-readerClosed
		if obj.detaches != 0 || obj.closes != 0 {
			sourceDone <- errors.New("source closed before source goroutine exited")
			return
		}
		sourceDone <- nil
	}()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = correlation.close(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("close() error = %v, want context canceled", err)
	}
	for _, want := range []error{readerErr, detachErr, objectErr} {
		if !errors.Is(err, want) {
			t.Fatalf("close() error = %v, want %v", err, want)
		}
	}
	if reader.closes != 1 || obj.detaches != 1 || obj.closes != 1 {
		t.Fatalf(
			"cleanup counts: detach=%d object_close=%d reader_close=%d",
			obj.detaches,
			obj.closes,
			reader.closes,
		)
	}
	if want := []string{"reader", "detach", "close"}; !slices.Equal(obj.operations, want) {
		t.Fatalf("object operations = %v, want %v", obj.operations, want)
	}
}
