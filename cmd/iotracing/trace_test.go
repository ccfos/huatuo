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
	"errors"
	"testing"

	"huatuo-bamai/internal/bpf"
)

var _ bpf.PerfEventReader = (*readerErrorAfterEvent)(nil)

type readerErrorAfterEvent struct {
	readErr error
	read    bool
}

func (r *readerErrorAfterEvent) ReadInto(data any) error {
	if r.read {
		return r.readErr
	}

	r.read = true
	event := data.(*bpfScheduleDelay)
	event.PID = 1
	event.Cost = 2500
	copy(event.Comm[:], "init")
	return nil
}

func (*readerErrorAfterEvent) ReadBatch(func() any) ([]any, error) {
	return nil, nil
}

func (*readerErrorAfterEvent) Close() error {
	return nil
}

func TestCollectStallsReturnsEventsBeforeReaderError(t *testing.T) {
	readerErr := errors.New("reader failed")
	stalls, err := collectStalls(
		&readerErrorAfterEvent{readErr: readerErr},
		1,
	)

	if !errors.Is(err, readerErr) {
		t.Fatalf("collectStalls() error = %v, want %v", err, readerErr)
	}
	if len(stalls) != 1 {
		t.Fatalf("collectStalls() returned %d stalls, want 1", len(stalls))
	}
	if stalls[0].Pid != 1 || stalls[0].Comm != "init" ||
		stalls[0].LatencyUs != 2 {
		t.Fatalf("collectStalls() stall = %+v", stalls[0])
	}
}
