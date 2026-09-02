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
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
)

const (
	testDropwatchPerfStatusMapID uint32 = 7
	testDropwatchRateLimitMapID  uint32 = 8
)

type dropwatchSourceBPFStub struct {
	bpf.BPF
	perfRaw    []byte
	rateRaw    []byte
	readErr    error
	readCalls  int
	detachErr  error
	closeErr   error
	operations []string
}

func (s *dropwatchSourceBPFStub) MapIDByName(name string) uint32 {
	switch name {
	case embeddedPerfStatusMapName:
		return testDropwatchPerfStatusMapID
	case embeddedRateLimitStateMapName:
		return testDropwatchRateLimitMapID
	}
	return 0
}

func (s *dropwatchSourceBPFStub) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	s.readCalls++
	if s.readErr != nil {
		return nil, s.readErr
	}
	if len(key) != 4 || binary.NativeEndian.Uint32(key) != 0 {
		return nil, errors.New("unexpected map read")
	}
	switch mapID {
	case testDropwatchPerfStatusMapID:
		return slices.Clone(s.perfRaw), nil
	case testDropwatchRateLimitMapID:
		return slices.Clone(s.rateRaw), nil
	}
	return nil, errors.New("unexpected map read")
}

func (s *dropwatchSourceBPFStub) Detach() error {
	s.operations = append(s.operations, "detach")
	return s.detachErr
}

func (s *dropwatchSourceBPFStub) Close() error {
	s.operations = append(s.operations, "object_close")
	return s.closeErr
}

type dropwatchSourceReaderStub struct {
	bpf.PerfEventReader
	records    []*abi.DropwatchPacketEvent
	errors     []error
	ctx        context.Context
	readCalls  int
	closeErr   error
	operations *[]string
}

func (s *dropwatchSourceReaderStub) ReadInto(destination any) error {
	s.readCalls++
	if len(s.errors) != 0 {
		err := s.errors[0]
		s.errors = s.errors[1:]
		return err
	}
	if len(s.records) != 0 {
		record, ok := destination.(*abi.DropwatchPacketEvent)
		if !ok {
			return errors.New("unexpected record type")
		}
		*record = *s.records[0]
		s.records = s.records[1:]
		return nil
	}
	if s.ctx != nil {
		<-s.ctx.Done()
		return s.ctx.Err()
	}
	return errors.New("no dropwatch records")
}

func (s *dropwatchSourceReaderStub) Close() error {
	if s.operations != nil {
		*s.operations = append(*s.operations, "reader_close")
	}
	return s.closeErr
}

func TestDropwatchSourceReadPerfStatus(t *testing.T) {
	object := &dropwatchSourceBPFStub{
		perfRaw: encodeDropwatchPerfStats(
			t,
			abi.BPFPerfOutputStats{Lost: 1},
			abi.BPFPerfOutputStats{Lost: 3},
		),
		rateRaw: encodeBPFRatelimitEvent(t, 6),
	}
	source := &dropwatchSource{
		object:            object,
		perfStatusMap:     testDropwatchPerfStatusMapID,
		rateLimitStateMap: testDropwatchRateLimitMapID,
	}

	status, err := source.readPerfStatus()
	if err != nil {
		t.Fatalf("readPerfStatus() error = %v", err)
	}
	if status.PerfLost != 4 || status.RateLimited != 6 {
		t.Fatalf("status = %+v, want perf_lost=4 rate_limited=6", status)
	}

	object.perfRaw = encodeDropwatchPerfStats(
		t,
		abi.BPFPerfOutputStats{Lost: 3},
	)
	if _, err := source.readPerfStatus(); err == nil ||
		!containsErrorText(err, "perf_lost regressed") {
		t.Fatalf("perf regression error = %v", err)
	}

	object.perfRaw = encodeDropwatchPerfStats(
		t,
		abi.BPFPerfOutputStats{Lost: 4},
	)
	object.rateRaw = encodeBPFRatelimitEvent(t, 5)
	if _, err := source.readPerfStatus(); err == nil ||
		!containsErrorText(err, "rate_limited regressed") {
		t.Fatalf("rate regression error = %v", err)
	}
}

func TestDropwatchSourceRejectsInvalidPerfStatus(t *testing.T) {
	readErr := errors.New("read failed")
	tests := []struct {
		name      string
		perfRaw   []byte
		rateRaw   []byte
		readErr   error
		wantError string
	}{
		{name: "empty", wantError: "value size 0"},
		{
			name:      "partial value",
			perfRaw:   make([]byte, abi.BPFPerfOutputStatsSize-1),
			wantError: "value size 7",
		},
		{
			name: "perf lost overflow",
			perfRaw: encodeDropwatchPerfStats(
				t,
				abi.BPFPerfOutputStats{Lost: math.MaxUint64},
				abi.BPFPerfOutputStats{Lost: 1},
			),
			wantError: "perf_lost overflow",
		},
		{name: "map read", readErr: readErr, wantError: "read failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &dropwatchSource{
				object: &dropwatchSourceBPFStub{
					perfRaw: test.perfRaw,
					rateRaw: test.rateRaw,
					readErr: test.readErr,
				},
				perfStatusMap:     testDropwatchPerfStatusMapID,
				rateLimitStateMap: testDropwatchRateLimitMapID,
			}
			_, err := source.readPerfStatus()
			if err == nil || !containsErrorText(err, test.wantError) {
				t.Fatalf("readPerfStatus() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestDropwatchSourceReadEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	record := newIPv4DropwatchTCPRecord(40)
	record.Meta.KtimeNS = 10
	record.Meta.NetNamespaceCookie = 1
	reader := &dropwatchSourceReaderStub{
		records: []*abi.DropwatchPacketEvent{record},
		errors:  []error{&bpf.PerfEventSamplesLostError{Count: 2}},
		ctx:     ctx,
	}
	source := &dropwatchSource{reader: reader}
	events := make(chan *dropEvent, 1)
	done := make(chan error, 1)
	go func() {
		done <- source.readEvents(ctx, events)
	}()

	event := <-events
	cancel()
	err := <-done
	if err != nil {
		t.Fatalf("readEvents() error = %v", err)
	}
	if event.ktimeNS != 10 || event.flow != testFlowKey(12345, 80) ||
		event.sequence != 123 || event.endSequence != 123 {
		t.Fatalf("event = %+v", event)
	}
	if reader.readCalls < 2 {
		t.Fatalf("ReadInto() calls = %d, want at least 2", reader.readCalls)
	}
	if got := source.lostSamples.Load(); got != 2 {
		t.Fatalf("lostSamples = %d, want 2", got)
	}
}

func TestDropwatchSourceCancellationDoesNotReadQueuedRecords(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := &dropwatchSourceReaderStub{
		records: []*abi.DropwatchPacketEvent{newIPv4DropwatchTCPRecord(40)},
	}
	source := &dropwatchSource{reader: reader}

	if err := source.readEvents(ctx, make(chan *dropEvent)); err != nil {
		t.Fatalf("readEvents() error = %v", err)
	}
	if reader.readCalls != 0 || len(reader.records) != 1 {
		t.Fatalf(
			"reader state = calls %d queued %d, want calls 0 queued 1",
			reader.readCalls,
			len(reader.records),
		)
	}
}

func TestDropwatchSourceClosePreservesErrorsAndOrder(t *testing.T) {
	detachErr := errors.New("detach failed")
	readerErr := errors.New("reader close failed")
	objectErr := errors.New("object close failed")
	object := &dropwatchSourceBPFStub{detachErr: detachErr, closeErr: objectErr}
	reader := &dropwatchSourceReaderStub{
		closeErr:   readerErr,
		operations: &object.operations,
	}
	source := &dropwatchSource{object: object, reader: reader}

	err := source.close()
	for _, target := range []error{detachErr, readerErr, objectErr} {
		if !errors.Is(err, target) {
			t.Fatalf("close() error = %v, want %v", err, target)
		}
	}
	if want := []string{"detach", "reader_close", "object_close"}; !slices.Equal(object.operations, want) {
		t.Fatalf("operations = %v, want %v", object.operations, want)
	}
}

func encodeDropwatchPerfStats(
	t *testing.T,
	values ...abi.BPFPerfOutputStats,
) []byte {
	t.Helper()
	raw := make([]byte, len(values)*abi.BPFPerfOutputStatsSize)
	for valueIndex, value := range values {
		offset := valueIndex * abi.BPFPerfOutputStatsSize
		if _, err := binary.Encode(
			raw[offset:offset+abi.BPFPerfOutputStatsSize],
			binary.NativeEndian,
			value,
		); err != nil {
			t.Fatalf("encode perf status: %v", err)
		}
	}
	return raw
}

func encodeBPFRatelimitEvent(t *testing.T, totalMissed uint64) []byte {
	t.Helper()
	raw := make([]byte, abi.BPFRatelimitEventSize)
	if _, err := binary.Encode(
		raw,
		binary.NativeEndian,
		abi.BPFRatelimitEvent{TotalMissed: totalMissed},
	); err != nil {
		t.Fatalf("encode rate limit state: %v", err)
	}
	return raw
}

func containsErrorText(err error, text string) bool {
	return err != nil && strings.Contains(err.Error(), text)
}
