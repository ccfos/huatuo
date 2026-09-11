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
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
)

const (
	testDropwatchPerfStatusMapID uint32 = 7
	testDropwatchRateLimitMapID  uint32 = 8
)

type tracerBPFStub struct {
	bpf.BPF
	perfRaw    []byte
	rateRaw    []byte
	readErr    error
	detachErr  error
	closeErr   error
	operations []string
}

func (s *tracerBPFStub) MapIDByName(name string) uint32 {
	switch name {
	case perfStatusMapName:
		return testDropwatchPerfStatusMapID
	case rateLimitStateMapName:
		return testDropwatchRateLimitMapID
	}
	return 0
}

func (s *tracerBPFStub) ReadMap(mapID uint32, key []byte) ([]byte, error) {
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

func (s *tracerBPFStub) Detach() error {
	s.operations = append(s.operations, "detach")
	return s.detachErr
}

func (s *tracerBPFStub) Close() error {
	s.operations = append(s.operations, "object_close")
	return s.closeErr
}

type tracerReaderStub struct {
	bpf.PerfEventReader
	records    []*abi.DropwatchPacketEvent
	errors     []error
	ctx        context.Context
	closeErr   error
	operations *[]string
	afterRead  func()
}

func (s *tracerReaderStub) ReadInto(destination any) error {
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
		if s.afterRead != nil {
			s.afterRead()
		}
		return nil
	}
	if s.ctx != nil {
		<-s.ctx.Done()
		return s.ctx.Err()
	}
	return errors.New("no dropwatch records")
}

func (s *tracerReaderStub) Close() error {
	if s.operations != nil {
		*s.operations = append(*s.operations, "reader_close")
	}
	return s.closeErr
}

func TestTracerReadStatus(t *testing.T) {
	object := &tracerBPFStub{
		perfRaw: encodeDropwatchPerfStats(
			t,
			abi.BPFPerfOutputStats{ErrorCounter: 1},
			abi.BPFPerfOutputStats{ErrorCounter: 3},
		),
		rateRaw: encodeBPFRatelimitEvent(t, 6),
	}
	source := &Tracer{
		bpf:               object,
		perfStatusMap:     testDropwatchPerfStatusMapID,
		rateLimitStateMap: testDropwatchRateLimitMapID,
	}

	status, err := source.ReadStatus()
	if err != nil {
		t.Fatalf("ReadStatus() error = %v", err)
	}
	if status.PerfLost != 4 || status.RateLimited != 6 {
		t.Fatalf("status = %+v, want perf_lost=4 rate_limited=6", status)
	}

	object.perfRaw = encodeDropwatchPerfStats(
		t,
		abi.BPFPerfOutputStats{ErrorCounter: 3},
	)
	object.rateRaw = encodeBPFRatelimitEvent(t, 5)
	status, err = source.ReadStatus()
	if err != nil || status.PerfLost != 3 || status.RateLimited != 5 {
		t.Fatalf("current snapshot = %+v, %v; want perf_lost=3 rate_limited=5, nil", status, err)
	}
}

func TestTracerRejectsInvalidPerfStatus(t *testing.T) {
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
		{name: "map read", readErr: readErr, wantError: "read failed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &Tracer{
				bpf: &tracerBPFStub{
					perfRaw: test.perfRaw,
					rateRaw: test.rateRaw,
					readErr: test.readErr,
				},
				perfStatusMap:     testDropwatchPerfStatusMapID,
				rateLimitStateMap: testDropwatchRateLimitMapID,
			}
			_, err := source.ReadStatus()
			if err == nil || !containsErrorText(err, test.wantError) {
				t.Fatalf("ReadStatus() error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestTracerClosePreservesErrorsAndOrder(t *testing.T) {
	detachErr := errors.New("detach failed")
	readerErr := errors.New("reader close failed")
	objectErr := errors.New("object close failed")
	object := &tracerBPFStub{detachErr: detachErr, closeErr: objectErr}
	reader := &tracerReaderStub{
		closeErr:   readerErr,
		operations: &object.operations,
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	source := &Tracer{bpf: object, reader: reader, ctx: ctx, cancel: cancel, limiter: bpf.NewRateLimiter("dropwatch", 0)}

	err := source.Close()
	for _, target := range []error{detachErr, readerErr, objectErr} {
		if !errors.Is(err, target) {
			t.Fatalf("close() error = %v, want %v", err, target)
		}
	}
	if want := []string{"detach", "reader_close", "object_close"}; !slices.Equal(object.operations, want) {
		t.Fatalf("operations = %v, want %v", object.operations, want)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("repeated Close() = %v, want nil", err)
	}
	if want := []string{"detach", "reader_close", "object_close"}; !slices.Equal(object.operations, want) {
		t.Fatalf("repeated Close() retried cleanup: %v", object.operations)
	}
}

func TestTracerReadStatusRateLimitSize(t *testing.T) {
	for _, size := range []int{0, 1, abi.BPFRatelimitEventSize - 1, abi.BPFRatelimitEventSize, abi.BPFRatelimitEventSize + 1} {
		tracer := &Tracer{
			bpf: &tracerBPFStub{
				perfRaw: make([]byte, abi.BPFPerfOutputStatsSize),
				rateRaw: make([]byte, size),
			},
			perfStatusMap:     testDropwatchPerfStatusMapID,
			rateLimitStateMap: testDropwatchRateLimitMapID,
		}
		tracer.lostSamples.Store(7)
		status, err := tracer.ReadStatus()
		wantError := size < abi.BPFRatelimitEventSize
		if (err != nil) != wantError || status.LostSamples != 7 {
			t.Fatalf("size %d: status=%+v error=%v, want lost_samples=7 error present=%v", size, status, err, wantError)
		}
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
