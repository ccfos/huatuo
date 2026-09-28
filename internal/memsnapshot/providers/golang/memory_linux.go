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

package golang

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	maxProcessReadRanges = 64
	maxProcessReadBytes  = 1 << 20
)

// processMemory owns syscall buffers for one snapshot; it is not shared.
type processMemory struct {
	pid       int
	local     [maxProcessReadRanges]unix.Iovec
	remote    [maxProcessReadRanges]unix.RemoteIovec
	headerRaw [bucketHeaderBytes]byte
}

type remoteRange struct {
	address uint64
	data    []byte
}

func validateReadRange(address uint64, data []byte) error {
	if len(data) == 0 || len(data) > maxProcessReadBytes || address == 0 {
		return errors.New("process memory read range is invalid")
	}
	last := address + uint64(len(data)-1)
	if last < address {
		return errors.New("process memory read range overflows")
	}
	return nil
}

// readInto requires a complete read; cancellation belongs to the caller.
func (m *processMemory) readInto(address uint64, dst []byte) error {
	if err := validateReadRange(address, dst); err != nil {
		return err
	}

	dataLen := len(dst)

	local := []unix.Iovec{{Base: &dst[0], Len: uint64(dataLen)}}
	remote := []unix.RemoteIovec{{Base: uintptr(address), Len: dataLen}}
	n, err := unix.ProcessVMReadv(m.pid, local, remote, 0)
	if err != nil {
		return err
	}
	if n != dataLen {
		return fmt.Errorf("short process memory read: got %d, want %d", n, dataLen)
	}
	return nil
}

// readBatch requires complete reads of at most maxProcessReadRanges ranges.
// On error, callers must discard all destinations, which may be partly written.
// Cancellation belongs to the caller.
func (m *processMemory) readBatch(ranges []remoteRange) error {
	rangeLen := len(ranges)

	local := m.local[:rangeLen]
	remote := m.remote[:rangeLen]
	total := 0
	for index := range ranges {
		item := &ranges[index]
		if err := validateReadRange(item.address, item.data); err != nil {
			return fmt.Errorf("process memory read range %d at %#x: %w", index, item.address, err)
		}

		local[index] = unix.Iovec{Base: &item.data[0], Len: uint64(len(item.data))}
		remote[index] = unix.RemoteIovec{Base: uintptr(item.address), Len: len(item.data)}
		total += len(item.data)
	}

	read, err := unix.ProcessVMReadv(m.pid, local, remote, 0)
	if err != nil {
		return err
	}
	if read != total {
		return fmt.Errorf("short process memory batch read: got %d, want %d", read, total)
	}
	return nil
}
