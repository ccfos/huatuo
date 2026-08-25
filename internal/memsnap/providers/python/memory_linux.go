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

package python

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	// Remote values must never be allowed to drive an unbounded allocation in
	// Huatuo. Current CPython metadata reads are at most a few KiB; these limits
	// leave ample headroom while containing corrupt or concurrently changed data.
	maxReadBytes = 1 << 20
)

type memoryReader interface {
	read(address uint64, size int) ([]byte, error)
	readInto(address uint64, destination []byte) error
}

type processMemory struct {
	pid int
	ctx context.Context
}

func newMemory(pid int, ctx context.Context) *processMemory {
	return &processMemory{pid: pid, ctx: ctx}
}

func (m *processMemory) read(address uint64, size int) ([]byte, error) {
	if err := validateRead(address, size); err != nil {
		return nil, err
	}
	data := make([]byte, size)
	if err := m.readInto(address, data); err != nil {
		return nil, err
	}
	return data, nil
}

// readInto is synchronous: cancellation is checked around a syscall whose
// buffer size is bounded but whose duration is not. Once ProcessVMReadv starts,
// it cannot be interrupted and may block without a time bound. After it returns,
// cancellation prevents later reads and no worker is left behind.
func (m *processMemory) readInto(address uint64, destination []byte) error {
	size := len(destination)
	if err := validateRead(address, size); err != nil {
		return err
	}
	if m.ctx != nil {
		if err := m.ctx.Err(); err != nil {
			return err
		}
	}
	local := [1]unix.Iovec{{Base: &destination[0], Len: uint64(size)}}
	remote := [1]unix.RemoteIovec{{Base: uintptr(address), Len: size}}
	read, err := unix.ProcessVMReadv(m.pid, local[:], remote[:], 0)
	if err != nil {
		return err
	}
	if m.ctx != nil {
		if err := m.ctx.Err(); err != nil {
			return err
		}
	}
	if read != size {
		return fmt.Errorf("short CPython process memory read: got %d, want %d",
			read, size)
	}
	return nil
}

func validateRead(address uint64, size int) error {
	if address == 0 || size <= 0 {
		return errors.New("CPython process memory range is invalid")
	}
	if size > maxReadBytes {
		return fmt.Errorf("CPython process memory read is too large: %d > %d bytes",
			size, maxReadBytes)
	}
	last := address + uint64(size-1)
	if last < address || uint64(uintptr(address)) != address ||
		uint64(uintptr(last)) != last {
		return errors.New("CPython process memory range overflows the address space")
	}
	return nil
}
