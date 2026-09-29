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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/unix"
)

// WithMemfdOutput gives the command an inherited anonymous memory file for output.
// maxBytes must be positive and bounds each read, not the child's file growth.
// During Start, argsForOutput receives its child-visible /proc/self/fd path and
// returns arguments to append to Spec.Args. It must be non-nil.
// This does not redirect stdout or stderr. New allocates no file; Wait and Run
// preserve it for MemfdOutput, and Stop closes it. Linux and /proc/self/fd are required.
func WithMemfdOutput(maxBytes int, argsForOutput func(outputPath string) []string) Option {
	return func(process *Process) {
		process.memfd = &memfdOutput{limit: maxBytes, argsForOutput: argsForOutput}
	}
}

// MemfdOutput copies the current file content from offset zero. Exceeding the
// configured limit returns that many prefix bytes and ErrOutputLimitExceeded.
// Read errors preserve any bytes already read and may accompany the limit error.
// Wait first for complete output. The returned data remains valid after Stop;
// subsequent reads after Stop return an error wrapping os.ErrClosed.
// Reading requires WithMemfdOutput and a successfully completed Start.
func (p *Process) MemfdOutput() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state == processStateInvalid {
		return nil, fmt.Errorf("read command memfd output: %w", errProcessNotInitialized)
	}
	if p.memfd == nil {
		return nil, fmt.Errorf("read command %q memfd output: WithMemfdOutput is not configured", p.spec.Path)
	}
	if p.state == processStateNew || p.state == processStateStarting {
		return nil, fmt.Errorf("read command %q memfd output: process has not been started", p.spec.Path)
	}
	if p.memfd.file == nil {
		return nil, fmt.Errorf("read command %q memfd output: %w", p.spec.Path, os.ErrClosed)
	}

	// Serialize the entire read with close; expose no borrowed file descriptor.
	info, err := p.memfd.file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat command %q memfd output: %w", p.spec.Path, err)
	}
	data, err := readMemfdOutput(p.memfd.file, info.Size(), p.memfd.limit)
	if err != nil {
		return data, fmt.Errorf("read command %q memfd output: %w", p.spec.Path, err)
	}
	return data, nil
}

func readMemfdOutput(reader io.ReaderAt, size int64, limit int) ([]byte, error) {
	if size == 0 {
		return nil, nil
	}
	var limitErr error
	if size > int64(limit) {
		limitErr = fmt.Errorf("%w: memfd output exceeds %d bytes", ErrOutputLimitExceeded, limit)
	}
	data := make([]byte, min(size, int64(limit)))
	n, readErr := reader.ReadAt(data, 0)
	return data[:n], errors.Join(limitErr, readErr)
}

type memfdOutput struct {
	limit         int
	argsForOutput func(string) []string
	file          *os.File
	closeErr      error
}

func (m *memfdOutput) prepare(cmd *exec.Cmd) error {
	fd, err := unix.MemfdCreate("huatuo-executil", unix.MFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("create command output memfd: %w", err)
	}
	m.file = os.NewFile(uintptr(fd), "huatuo-executil")
	outputPath := "/proc/self/fd/" + strconv.Itoa(3+len(cmd.ExtraFiles))
	cmd.Args = append(cmd.Args, m.argsForOutput(outputPath)...)
	cmd.ExtraFiles = append(cmd.ExtraFiles, m.file)
	return validateArgs(cmd.Args[1:])
}

// close is called under Process.mu, including after a failed start.
func (m *memfdOutput) close() error {
	if m.file != nil {
		if err := m.file.Close(); err != nil {
			m.closeErr = fmt.Errorf("close command output memfd: %w", err)
		}
		m.file = nil
	}
	return m.closeErr
}

func (p *Process) closeMemfd() error {
	if p.memfd == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.memfd.close()
}
