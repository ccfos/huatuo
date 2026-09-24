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

//go:build integration && linux

// These cover the fixture's collection window without a kernel: a stop request
// that lands inside a read is what a caller measuring traffic depends on, and a
// real kernel cannot be asked to lose that race on demand.
package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/pkg/types"
)

// readResult is what one ReadBatch call of the stub returns.
type readResult struct {
	batch bpf.PerfEventBatch
	err   error
}

// readerStub returns the read results it was given, one per call.
type readerStub struct {
	results []readResult
	reads   int
}

func (r *readerStub) ReadInto(any) error { return nil }

func (r *readerStub) ReadBatch(newEvent func() any) (bpf.PerfEventBatch, error) {
	if r.reads >= len(r.results) {
		return bpf.PerfEventBatch{}, nil
	}

	next := r.results[r.reads]
	r.reads++

	return next.batch, next.err
}

func (r *readerStub) Close() error { return nil }

// stoppingReader reports what a stop request produces mid-read: the first read
// returns normally, the next one cancels the context and returns the exit
// sentinel together with the events that read had already picked up.
type stoppingReader struct {
	*readerStub
	cancel context.CancelFunc
}

func (r *stoppingReader) ReadBatch(newEvent func() any) (bpf.PerfEventBatch, error) {
	if r.reads == 0 {
		return r.readerStub.ReadBatch(newEvent)
	}

	r.cancel()

	return bpf.PerfEventBatch{Events: []any{eventFor()}, LostSamples: 2}, types.ErrExitByCancelCtx
}

// eventFor returns an event of the stage the fixture reports first.
func eventFor() *abi.NetRXLatencyEvent {
	return &abi.NetRXLatencyEvent{LatencyStage: 1, LatencyNS: 1}
}

// testConfig keeps the window open for the batches the stubs return.
func testConfig() config {
	return config{timeout: time.Second, maxEvents: 8}
}

// TestCollectEventsKeepsTheBatchAStopInterrupted is the deterministic half of a
// SIGTERM during a read: the reader reports the cancellation with the events it
// already had, and the fixture must count them and stop, not fail without a
// summary.
func TestCollectEventsKeepsTheBatchAStopInterrupted(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reader := &stoppingReader{
		readerStub: &readerStub{results: []readResult{{
			batch: bpf.PerfEventBatch{Events: []any{eventFor()}, LostSamples: 3},
		}}},
		cancel: cancel,
	}

	observed, err := collectEvents(ctx, reader, kprobeProgram, testConfig())
	if err != nil {
		t.Fatalf("a stop request must not fail the collection: %v", err)
	}
	if observed.events != 2 {
		t.Fatalf("collected %d events, want 2 (one per batch)", observed.events)
	}
	if observed.lost != 5 {
		t.Fatalf("lost %d samples, want 5 (3 from the first batch, 2 from the interrupted read)",
			observed.lost)
	}
	if observed.timedOut {
		t.Fatal("a stop request is not a timeout")
	}
	if len(observed.stages) != 1 {
		t.Fatalf("stages = %v, want the one stage both events reported", observed.stages)
	}
}

// TestCollectEventsStillFailsOnAReadError keeps the other half: an error that is
// not the stop request stays an error, whatever the batch carried.
func TestCollectEventsStillFailsOnAReadError(t *testing.T) {
	t.Parallel()

	readErr := fmt.Errorf("read perf event: %w", errors.New("bad address"))

	reader := &readerStub{results: []readResult{{
		batch: bpf.PerfEventBatch{Events: []any{eventFor()}},
		err:   readErr,
	}}}

	_, err := collectEvents(context.Background(), reader, kprobeProgram, testConfig())
	if !errors.Is(err, readErr) {
		t.Fatalf("error = %v, want the read error %v", err, readErr)
	}
}
