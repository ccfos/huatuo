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
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

const (
	mbucketBatchSize    = 64
	maxProcessReadBytes = 1 << 20
)

type processMemory struct {
	pid int
	ctx context.Context
}

type bucketRead struct {
	recordAddress uint64
	stackAddress  uint64
	stackDepth    int
	recordRaw     [memRecordBytes]byte
	objects       int64
	bytes         int64
}

type remoteRange struct {
	address uint64
	data    []byte
}

// batchWorkspace keeps all transient batch buffers bounded and reusable for
// one capture. The largest member is the 32 KiB stack slab.
type batchWorkspace struct {
	buckets      [mbucketBatchSize]bucketRead
	recordRanges [mbucketBatchSize]remoteRange
	stackRanges  [mbucketBatchSize]remoteRange
	stackBuckets [mbucketBatchSize]int
	readable     [mbucketBatchSize]bool
	local        [mbucketBatchSize]unix.Iovec
	remote       [mbucketBatchSize]unix.RemoteIovec
	stackRaw     [mbucketBatchSize * maxStackDepth * 8]byte
}

func (m processMemory) readInto(address uint64, data []byte) error {
	if err := m.ctx.Err(); err != nil {
		return err
	}
	if len(data) == 0 || len(data) > maxProcessReadBytes || address == 0 {
		return errors.New("process memory read range is invalid")
	}
	last := address + uint64(len(data)-1)
	if last < address || uint64(uintptr(address)) != address ||
		uint64(uintptr(last)) != last {
		return errors.New("process memory read range overflows")
	}
	local := [1]unix.Iovec{{Base: &data[0], Len: uint64(len(data))}}
	remote := [1]unix.RemoteIovec{{Base: uintptr(address), Len: len(data)}}
	read, err := unix.ProcessVMReadv(m.pid, local[:], remote[:], 0)
	if err != nil {
		return err
	}
	if err := m.ctx.Err(); err != nil {
		return err
	}
	if read != len(data) {
		return fmt.Errorf("short process memory read: got %d, want %d", read,
			len(data))
	}
	return nil
}

// readProcessRanges combines independent victim ranges into one
// process_vm_readv call. If a range changed concurrently and causes a partial
// batch read, retry the small batch range-by-range so one bad mbucket does not
// discard its readable neighbors.
func (workspace *batchWorkspace) readProcessRanges(memory processMemory,
	ranges []remoteRange,
) []bool {
	readable := workspace.readable[:len(ranges)]
	clear(readable)
	if len(ranges) == 0 {
		return readable
	}
	local := workspace.local[:len(ranges)]
	remote := workspace.remote[:len(ranges)]
	total := 0
	valid := true
	for index := range ranges {
		rangeToRead := &ranges[index]
		if len(rangeToRead.data) == 0 || rangeToRead.address == 0 {
			valid = false
			break
		}
		local[index] = unix.Iovec{
			Base: &rangeToRead.data[0], Len: uint64(len(rangeToRead.data)),
		}
		remote[index] = unix.RemoteIovec{
			Base: uintptr(rangeToRead.address), Len: len(rangeToRead.data),
		}
		total += len(rangeToRead.data)
	}
	if valid && total <= maxProcessReadBytes {
		if err := memory.ctx.Err(); err != nil {
			return readable
		}
		read, err := unix.ProcessVMReadv(memory.pid, local, remote, 0)
		if memory.ctx.Err() != nil {
			return readable
		}
		if err == nil && read == total {
			for index := range readable {
				readable[index] = true
			}
			return readable
		}
	}
	for index := range ranges {
		readable[index] = memory.readInto(ranges[index].address,
			ranges[index].data) == nil
	}
	return readable
}
