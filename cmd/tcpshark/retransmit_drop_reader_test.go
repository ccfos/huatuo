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
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
)

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
