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

package exec

import (
	"slices"
	"sync"
)

const maxErrorOutputBytes = 64 << 10

type outputBuffer struct {
	mu       sync.Mutex
	limit    int
	data     []byte
	exceeded bool
	onExceed func()
}

func newOutputBuffer(limit int) outputBuffer {
	return outputBuffer{limit: limit}
}

func (b *outputBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()

	written := len(data)
	remaining := b.limit - len(b.data)
	if remaining <= 0 {
		b.exceeded = b.exceeded || written > 0
	} else if written > remaining {
		b.data = append(b.data, data[:remaining]...)
		b.exceeded = true
	} else {
		b.data = append(b.data, data...)
	}

	// The first write past the limit hands the callback to the caller. It runs
	// outside the lock: Write is called by the os/exec copier goroutine, so the
	// callback must never wait on this buffer.
	var onExceed func()
	if b.exceeded && b.onExceed != nil {
		onExceed = b.onExceed
		b.onExceed = nil
	}
	b.mu.Unlock()

	if onExceed != nil {
		onExceed()
	}
	return written, nil
}

// setOnExceed registers the callback that runs once a write passes the retained
// limit. A buffer that has already passed it runs the callback immediately, so a
// command cannot overflow before the registration and escape it.
func (b *outputBuffer) setOnExceed(onExceed func()) {
	b.mu.Lock()
	if b.exceeded {
		b.mu.Unlock()
		onExceed()
		return
	}
	b.onExceed = onExceed
	b.mu.Unlock()
}

func (b *outputBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	return slices.Clone(b.data)
}

func (b *outputBuffer) Exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.exceeded
}

type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	start int
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	written := len(data)
	if written == 0 {
		return 0, nil
	}
	if written >= maxErrorOutputBytes {
		if cap(b.data) < maxErrorOutputBytes {
			b.data = make([]byte, maxErrorOutputBytes)
		} else {
			b.data = b.data[:maxErrorOutputBytes]
		}
		copy(b.data, data[written-maxErrorOutputBytes:])
		b.start = 0
		return written, nil
	}

	if len(b.data) < maxErrorOutputBytes {
		remaining := maxErrorOutputBytes - len(b.data)
		if written <= remaining {
			b.data = append(b.data, data...)
			return written, nil
		}
		b.data = append(b.data, data[:remaining]...)
		data = data[remaining:]
	}

	first := min(len(data), maxErrorOutputBytes-b.start)
	copy(b.data[b.start:], data[:first])
	copy(b.data, data[first:])
	b.start = (b.start + len(data)) % maxErrorOutputBytes
	return written, nil
}

func (b *tailBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.data) < maxErrorOutputBytes || b.start == 0 {
		return slices.Clone(b.data)
	}

	data := make([]byte, len(b.data))
	offset := copy(data, b.data[b.start:])
	copy(data[offset:], b.data[:b.start])
	return data
}
