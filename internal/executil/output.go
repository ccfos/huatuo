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

package executil

import (
	"fmt"
	"io"
	"slices"
	"sync"
)

const maxErrorOutputBytes = 64 << 10

type outputBuffer struct {
	mu               sync.Mutex
	limit            int
	data             []byte
	hasExceededLimit bool
}

func (b *outputBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	written := len(data)
	retained := min(written, b.limit-len(b.data))
	b.data = append(b.data, data[:retained]...)
	b.hasExceededLimit = b.hasExceededLimit || retained < written

	return written, nil
}

// Snapshot keeps the retained prefix and its overflow flag consistent.
func (b *outputBuffer) Snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.data), b.hasExceededLimit
}

type tailBuffer struct {
	mu           sync.Mutex
	data         []byte
	start        int
	hasTruncated bool
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	written := len(data)
	if written == 0 {
		return 0, nil
	}
	b.hasTruncated = b.hasTruncated || written > maxErrorOutputBytes-len(b.data)

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
	data, _ := b.Snapshot()
	return data
}

func (b *tailBuffer) Snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.data) < maxErrorOutputBytes || b.start == 0 {
		return slices.Clone(b.data), b.hasTruncated
	}

	data := make([]byte, len(b.data))
	offset := copy(data, b.data[b.start:])
	copy(data[offset:], b.data[:b.start])
	return data, b.hasTruncated
}

// WithStdout redirects standard output instead of retaining it for Stdout.
// A nil writer preserves capture. The caller owns the writer until Wait returns.
// Write must return promptly; the process cannot interrupt a blocked writer.
func WithStdout(writer io.Writer) Option {
	return func(process *Process) {
		process.stdoutWriter = writer
	}
}

// WithStderr redirects standard error instead of retaining it for Stderr.
// A nil writer preserves capture. The caller owns the writer until Wait returns.
// Write must return promptly; the process cannot interrupt a blocked writer.
func WithStderr(writer io.Writer) Option {
	return func(process *Process) {
		process.stderrWriter = writer
	}
}

// Stdout returns a copy of the retained standard output.
// It is empty when WithStdout redirects output to a non-nil writer. Exceeding
// Spec.MaxOutputBytes returns the retained prefix and ErrOutputLimitExceeded.
// Wait first for complete output; snapshots remain available after Stop.
func (p *Process) Stdout() ([]byte, error) {
	// The limit is immutable after New and distinguishes an uninitialized Process.
	if p.output.limit == 0 {
		return nil, fmt.Errorf("read command stdout: %w", errProcessNotInitialized)
	}

	data, exceeded := p.output.Snapshot()
	if exceeded {
		return data, fmt.Errorf(
			"%w: command %q stdout exceeds %d bytes",
			ErrOutputLimitExceeded,
			p.spec.Path,
			p.spec.MaxOutputBytes,
		)
	}

	return data, nil
}

// Stderr returns a copy of the newest 64 KiB written to standard error.
// It is empty when WithStderr redirects output to a non-nil writer.
func (p *Process) Stderr() []byte {
	return p.stderr.Bytes()
}
