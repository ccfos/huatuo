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
	"bytes"
	"io"
	"sync"
)

type outputBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	maxBytes int
}

func (b *outputBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	written := len(data)
	if written >= b.maxBytes {
		b.buf.Reset()
		data = data[written-b.maxBytes:]
	} else if discard := written - (b.maxBytes - b.buf.Len()); discard > 0 {
		b.buf.Next(discard)
	}

	_, err := b.buf.Write(data)
	return written, err
}

// Bytes copies the retained output so callers can keep it while writes continue.
func (b *outputBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

// WithStdout redirects standard output instead of retaining it for Stdout.
// A nil writer preserves capture. The caller owns the writer until Wait returns.
// Write must return promptly; the process cannot interrupt a blocked writer.
func WithStdout(writer io.Writer) Option {
	return func(process *Process) {
		process.stdout = writer
	}
}

// WithStderr redirects standard error instead of retaining it for Stderr.
// A nil writer preserves capture. The caller owns the writer until Wait returns.
// Write must return promptly; the process cannot interrupt a blocked writer.
func WithStderr(writer io.Writer) Option {
	return func(process *Process) {
		process.stderr = writer
	}
}

// Stdout returns a copy of the newest Spec.MaxStdoutBytes of standard output.
// It is empty when WithStdout redirects output to a non-nil writer.
// Wait first for the final tail; snapshots remain available after Stop.
func (p *Process) Stdout() []byte {
	if buffer, ok := p.stdout.(*outputBuffer); ok {
		return buffer.Bytes()
	}
	return nil
}

// Stderr returns a copy of the newest Spec.MaxStderrBytes of standard error.
// It is empty when WithStderr redirects output to a non-nil writer.
// Wait first for the final tail; snapshots remain available after Stop.
func (p *Process) Stderr() []byte {
	if buffer, ok := p.stderr.(*outputBuffer); ok {
		return buffer.Bytes()
	}
	return nil
}
