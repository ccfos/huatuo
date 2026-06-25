// Copyright 2025 The HuaTuo Authors
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

package bpf

import (
	"context"
	"fmt"

	"huatuo-bamai/internal/log"
)

const BpfDbgMsgLen = 64

// BpfDbgEvent mirrors struct bpf_dbg_event in bpf_dbg.h.
// The binary layout must match exactly (112 bytes, no padding).
type BpfDbgEvent struct {
	Timestamp uint64
	FileID    uint32
	Line      uint32
	Args      [4]uint64
	Msg       [BpfDbgMsgLen]byte
}

// ReadDbgEvent reads a single debug event from the perf event reader.
func ReadDbgEvent(reader PerfEventReader) (*BpfDbgEvent, error) {
	var event BpfDbgEvent
	if err := reader.ReadInto(&event); err != nil {
		return nil, fmt.Errorf("read debug event: %w", err)
	}
	return &event, nil
}

// DebugEventLoop reads debug events in a loop and logs each at Debug level.
// Blocks until ctx is canceled or the reader encounters a fatal error.
func DebugEventLoop(ctx context.Context, reader PerfEventReader) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		event, err := ReadDbgEvent(reader)
		if err != nil {
			return err
		}

		log.Debugf(
			"bpf_dbg: file=%#x line=%d ts=%d msg=%s args=[%#x %#x %#x %#x]",
			event.FileID, event.Line, event.Timestamp,
			nullTerminatedString(event.Msg[:]),
			event.Args[0], event.Args[1], event.Args[2], event.Args[3],
		)
	}
}

func nullTerminatedString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
