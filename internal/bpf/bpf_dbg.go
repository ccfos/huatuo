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

// BpfDbg controls whether the BPF objects it is associated with have their
// debug output compiled in via constant rewrite. Each instance owns its own
// enabled flag, so multiple BPF objects loaded within the same process can
// have their debug control toggled independently. Obtain one via NewDbg.
//
// The zero value is a valid, debug-disabled BpfDbg.
type BpfDbg struct {
	enabled bool
}

// NewDbg returns a BpfDbg whose debug output is controlled by enabled.
// Pass the result to LoadBpf via WithBpfDbg and to StartDebugEventLoop so a
// single BPF object's debug state stays isolated from other objects.
func NewDbg(enabled bool) *BpfDbg {
	return &BpfDbg{enabled: enabled}
}

// Enabled reports whether this BpfDbg has debug output turned on.
func (d *BpfDbg) Enabled() bool {
	return d != nil && d.enabled
}

// WithBpfDbg injects the bpf_dbg_enabled constant into consts when this
// BpfDbg has debug enabled, so callers can fold it into the map passed to
// LoadBpf:
//
//	dbg := bpf.NewDbg(enable)
//	b, err := bpf.LoadBpf("x.o", dbg.WithBpfDbg(map[string]any{...}))
//
// When debug is disabled consts is returned unchanged.
//
// bpf_dbg_enabled is always defined in bpf_dbg.h (outside the DEBUG_BPF
// guard), so RewriteConstants can rewrite it regardless of how the object
// was compiled. In non-debug builds nothing references it, so flipping it
// to 1 is a harmless no-op; in debug builds it gates bpf_dbg() output.
func (d *BpfDbg) WithBpfDbg(consts map[string]any) map[string]any {
	if !d.Enabled() {
		return consts
	}

	if consts == nil {
		consts = make(map[string]any)
	}
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

// debugEventLoop reads debug events in a loop and logs each at Debug level.
// Blocks until ctx is canceled or the reader encounters a fatal error.
//
// It is a no-op unless this BpfDbg was created with debug enabled before
// loading. When disabled the debug perf event array is either absent
// (non-DEBUG_BPF build) or never written to (run-time gate off), so reading
// it would block forever or operate on an invalid reader; the guard prevents
// that misuse.
func (d *BpfDbg) debugEventLoop(ctx context.Context, reader PerfEventReader) error {
	if !d.Enabled() {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var event BpfDbgEvent
		if err := reader.ReadInto(&event); err != nil {
			return fmt.Errorf("read debug event: %w", err)
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
// running the debug event loop against it. Returns a cleanup function the
// caller must defer; it cancels the loop and closes the reader, then blocks
// until the goroutine has exited so no reads outlive the cleanup.
//
// It is a no-op in either of these cases, returning a do-nothing cleanup and
// no error so callers can use it unconditionally:
//
//   - BPF debug is disabled (this BpfDbg was created with enabled=false).
//   - mapName does not exist in the loaded object (MapIDByName == 0, the
//     documented "not found" sentinel). This can happen when the object
//     was built without -DDEBUG_BPF (BPF_DBG_MAP is elided), when the
//     source omits BPF_DBG_MAP(...), or when the caller passes a wrong name.
func (d *BpfDbg) StartDebugEventLoop(ctx context.Context, b BPF, mapName string) (func(), error) {
	if !d.Enabled() {
		return func() {}, nil
	}

	if b.MapIDByName(mapName) == 0 {
		log.Debugf("bpf debug map %q not found, skipping event loop "+
			"(check -DDEBUG_BPF build flag, BPF_DBG_MAP declaration, and map name)",
			mapName)
		return func() {}, nil
	}

	loopCtx, cancel := context.WithCancel(ctx)

	reader, err := b.EventPipeByName(loopCtx, mapName, 4096)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open bpf debug map %q: %w", mapName, err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := d.debugEventLoop(loopCtx, reader); err != nil && loopCtx.Err() == nil {
			log.Warnf("bpf debug event loop %q: %v", mapName, err)
		}
	}()

	return func() {
		cancel()
		reader.Close()
		<-done
	}, nil
}
