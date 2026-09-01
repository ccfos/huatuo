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
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
)

type readPerfEventsReaderStub struct {
	bpf.PerfEventReader
	readCalls int
	read      func(any) error
}

func (s *readPerfEventsReaderStub) ReadInto(destination any) error {
	s.readCalls++
	return s.read(destination)
}

func TestReadPerfEventsRetriesLostSamples(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	reader := &readPerfEventsReaderStub{}
	reader.read = func(destination any) error {
		if reader.readCalls == 1 {
			return &bpf.PerfEventSamplesLostError{Count: 2}
		}
		record, ok := destination.(*uint64)
		if !ok {
			return errors.New("unexpected record type")
		}
		*record = 42
		return nil
	}

	var consumed []uint64
	err := readPerfEvents[uint64](ctx, reader, "test", func(record *uint64) error {
		consumed = append(consumed, *record)
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("readPerfEvents() error = %v", err)
	}
	if reader.readCalls != 2 {
		t.Fatalf("ReadInto() calls = %d, want 2", reader.readCalls)
	}
	if len(consumed) != 1 || consumed[0] != 42 {
		t.Fatalf("consumed records = %v, want [42]", consumed)
	}
}

func TestReadPerfEventsStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	readErr := errors.New("reader closed")
	reader := &readPerfEventsReaderStub{read: func(any) error {
		cancel()
		return readErr
	}}

	err := readPerfEvents[uint64](ctx, reader, "test", func(*uint64) error {
		t.Fatal("consume called after context cancellation")
		return nil
	})
	if err != nil {
		t.Fatalf("readPerfEvents() error = %v, want nil", err)
	}
	if reader.readCalls != 1 {
		t.Fatalf("ReadInto() calls = %d, want 1", reader.readCalls)
	}
}

func TestReadPerfEventsReturnsNamedReadError(t *testing.T) {
	readErr := errors.New("read failed")
	reader := &readPerfEventsReaderStub{read: func(any) error {
		return readErr
	}}

	err := readPerfEvents[uint64](t.Context(), reader, "test event", func(*uint64) error {
		t.Fatal("consume called after read failure")
		return nil
	})
	if !errors.Is(err, readErr) {
		t.Fatalf("readPerfEvents() error = %v, want %v", err, readErr)
	}
	if !strings.Contains(err.Error(), "read test event") {
		t.Fatalf("readPerfEvents() error = %v, want event name", err)
	}
}
