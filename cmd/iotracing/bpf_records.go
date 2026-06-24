// Copyright 2025, 2026 The HuaTuo Authors
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

package main

import (
	"strings"

	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/utils/bytesutil"
)

// iocbDirect mirrors the kernel IOCB_DIRECT flag in iocb.ki_flags;
// captured by the BPF program into bpfFilesystemIO.Flags.
const iocbDirect = 1 << 2

// bpfBlockLatency mirrors the per-IO latency aggregate written by the BPF
// program; field order matches the C struct so binary.Read works.
type bpfBlockLatency struct {
	Count    uint64
	MaxD2CNs uint64
	SumD2CNs uint64
	MaxQ2CNs uint64
	SumQ2CNs uint64
}

// bpfFilesystemIO mirrors one io_source_map entry: per-file IO totals,
// latency, comm and dentry path captured during the trace window.
type bpfFilesystemIO struct {
	Tgid            uint32
	Pid             uint32
	DevID           uint32
	Flags           uint32
	FsWriteBytes    uint64
	FsReadBytes     uint64
	BlockWriteBytes uint64
	BlockReadBytes  uint64
	Ino             uint64
	BlkcgID         uint64
	Latency         bpfBlockLatency
	Comm            [16]byte
	PathSegs        [8][32]byte
}

// bpfScheduleDelay mirrors one iodelay_perf_events ring-buffer record: a
// task that stalled in io_schedule longer than the threshold, with
// the kernel stack captured at the stall.
type bpfScheduleDelay struct {
	Stack      [symbol.KsymStackMinDepth]uint64
	Timestamp  uint64
	LatencyNs  uint64
	StackDepth uint32
	Pid        uint32
	Tid        uint32
	CPU        uint32
	Comm       [16]byte
}

// IsDirect reports whether the IO bypassed the page cache.
func (r *bpfFilesystemIO) IsDirect() bool {
	return r.Ino == 0 || r.Flags&iocbDirect != 0
}

// PathName reconstructs the absolute file path from the BPF dentry walk.
// Empty when the BPF entry has no inode (typical for direct IO that
// never reached an address_space).
func (r *bpfFilesystemIO) PathName() string {
	if r.Ino == 0 {
		return ""
	}

	names := make([]string, 0, len(r.PathSegs))
	for i := len(r.PathSegs) - 1; i >= 0; i-- {
		s := strings.TrimSpace(bytesutil.ToStr(r.PathSegs[i][:]))
		if s == "" || s == "/" {
			continue
		}

		names = append(names, s)
	}

	return "/" + strings.Join(names, "/")
}
