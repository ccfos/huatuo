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

package java

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/memsnap"
)

const (
	maxReadIOVs  = 128
	maxReadBytes = 1 << 20
)

type processMemory struct {
	pid         int
	ctx         context.Context
	deadline    time.Time
	hasDeadline bool
}

type memoryRange struct {
	address uint64
	size    int
}

func (m processMemory) check() error {
	if m.ctx != nil {
		if err := m.ctx.Err(); err != nil {
			return err
		}
	}
	if memsnap.DeadlineReached(m.deadline, m.hasDeadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (m processMemory) read(address uint64, size int) ([]byte, error) {
	if err := m.check(); err != nil {
		return nil, err
	}
	if address == 0 || size <= 0 || size > maxReadBytes {
		return nil, errors.New("HotSpot memory read range is invalid")
	}
	if _, ok := checkedAdd(address, uint64(size)); !ok {
		return nil, errors.New("HotSpot memory read range overflows")
	}
	data := make([]byte, size)
	local := []unix.Iovec{{Base: &data[0], Len: uint64(size)}}
	remote := []unix.RemoteIovec{{Base: uintptr(address), Len: size}}
	read, err := unix.ProcessVMReadv(m.pid, local, remote, 0)
	if err != nil {
		return nil, err
	}
	if err := m.check(); err != nil {
		return nil, err
	}
	if read != size {
		return nil, fmt.Errorf("short HotSpot memory read: got %d, want %d", read, size)
	}
	return data, nil
}

func (m processMemory) readv(ranges []memoryRange) ([][]byte, error) {
	if err := m.check(); err != nil {
		return nil, err
	}
	if len(ranges) == 0 {
		return nil, nil
	}
	if len(ranges) > maxReadIOVs {
		return nil, errors.New("HotSpot vector memory read has too many ranges")
	}
	data := make([][]byte, len(ranges))
	local := make([]unix.Iovec, len(ranges))
	remote := make([]unix.RemoteIovec, len(ranges))
	want := 0
	for index, item := range ranges {
		if item.address == 0 || item.size <= 0 {
			return nil, errors.New("HotSpot vector memory read range is invalid")
		}
		if item.size > maxReadBytes-want {
			return nil, errors.New("HotSpot vector memory read is too large")
		}
		if _, ok := checkedAdd(item.address, uint64(item.size)); !ok {
			return nil, errors.New("HotSpot vector memory read range overflows")
		}
		data[index] = make([]byte, item.size)
		local[index] = unix.Iovec{Base: &data[index][0], Len: uint64(item.size)}
		remote[index] = unix.RemoteIovec{Base: uintptr(item.address), Len: item.size}
		want += item.size
	}
	read, err := unix.ProcessVMReadv(m.pid, local, remote, 0)
	if err != nil {
		return nil, err
	}
	if err := m.check(); err != nil {
		return nil, err
	}
	if read != want {
		return nil, fmt.Errorf("short HotSpot vector memory read: got %d, want %d",
			read, want)
	}
	return data, nil
}

func (m processMemory) uint64(address uint64) (uint64, error) {
	raw, err := m.read(address, 8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(raw), nil
}

func (m processMemory) uint32(address uint64) (uint32, error) {
	raw, err := m.read(address, 4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(raw), nil
}

func (m processMemory) cstring(address uint64) (string, error) {
	if address == 0 {
		return "", nil
	}
	result := make([]byte, 0, 64)
	for len(result) < maxHotSpotStringBytes {
		chunkSize := 64
		if remaining := maxHotSpotStringBytes - len(result); remaining < chunkSize {
			chunkSize = remaining
		}
		chunk, err := m.read(address+uint64(len(result)), chunkSize)
		if err != nil {
			return "", err
		}
		for _, value := range chunk {
			if value == 0 {
				return string(result), nil
			}
			result = append(result, value)
		}
	}
	return "", unsupportedHotSpot("HotSpot metadata string exceeds safety limit")
}

func checkedAdd(left, right uint64) (uint64, bool) {
	if right > ^uint64(0)-left {
		return 0, false
	}
	return left + right, true
}
