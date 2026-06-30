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
	"huatuo-bamai/internal/timeutil"
)

const (
	BpfDbgMsgLen  = 64
	BpfDbgFileLen = 64
)

// bpfDbgEnabled is the process-wide switch controlling whether newly loaded
// BPF objects have their debug output compiled in via constant rewrite.
var bpfDbgEnabled bool

// EnableBpfDbg turns on BPF debug output for subsequently loaded BPF objects.
// It must be called before LoadBpf; already-loaded objects are unaffected.
func EnableBpfDbg() { bpfDbgEnabled = true }

// WithBpfDbg injects the bpf_dbg_enabled constant into consts when BPF debug
// is enabled, so callers can fold it into the map passed to LoadBpf:
//
//	b, err := bpf.LoadBpf("x.o", bpf.WithBpfDbg(map[string]any{...}))
//
// When debug is disabled consts is returned unchanged.
func WithBpfDbg(consts map[string]any) map[string]any {
	if !bpfDbgEnabled {
		return consts
	}

	if consts == nil {
		consts = make(map[string]any)
	}
	// bpf_dbg_enabled is the volatile const u32 in bpf_dbg.h that gates
	// bpf_dbg() output; RewriteConstants sets it at load time.
	consts["bpf_dbg_enabled"] = uint32(1)

	return consts
}

// BpfDbgEvent mirrors struct bpf_dbg_event in bpf_dbg.h.
type BpfDbgEvent struct {
	FileName  [BpfDbgFileLen]byte
	FileLine  uint32
	Pad0      uint32
	Msg       [BpfDbgMsgLen]byte
	Args      [4]uint64
	Timestamp uint64
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

		ts, err := timeutil.KtimeToTime(event.Timestamp)
		if err != nil {
			return fmt.Errorf("convert bpf timestamp: %w", err)
		}

		args := ""
		if event.Args != [4]uint64{} {
			args = fmt.Sprintf(" args=[%#x %#x %#x %#x]",
				event.Args[0], event.Args[1], event.Args[2], event.Args[3])
		}

		log.Debugf(
			"bpf_dbg: file=%s line=%d ts=%s msg=%s%s",
			nullTerminatedString(event.FileName[:]), event.FileLine,
			timeutil.FormatUTC(ts),
			nullTerminatedString(event.Msg[:]),
			args,
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

// StartDebugEventLoop opens mapName as a perf event pipe and spawns a goroutine
// running DebugEventLoop against it. It is a no-op when BPF debug is not
// enabled. Returns a cleanup function the caller must defer to close the reader.
func StartDebugEventLoop(ctx context.Context, b BPF, mapName string) (func(), error) {
	if !bpfDbgEnabled {
		return func() {}, nil
	}

	reader, err := b.EventPipeByName(ctx, mapName, 4096)
	if err != nil {
		return nil, fmt.Errorf("open bpf debug map %q: %w", mapName, err)
	}

	go func() {
		if err := DebugEventLoop(ctx, reader); err != nil && ctx.Err() == nil {
			log.Warnf("bpf debug event loop %q: %v", mapName, err)
		}
	}()

	return func() { reader.Close() }, nil
}
