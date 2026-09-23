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

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
)

// IOCB_DIRECT bit in iocb.ki_flags, captured by the BPF program into
// bpfFilesystemIO.Flags. Its value is kernel-version dependent:
//
//	< 5.10:  1 << 2  (include/linux/fs.h v4.19:298, v5.4:311, v5.9:315)
//	>= 5.10: 1 << 17 (include/linux/fs.h v5.10:314, v6.1:333, v6.6:334)
//
// v5.10 rewrote the IOCB_* flags as aliases of RWF_*: bit 2 became IOCB_SYNC
// (= RWF_SYNC, 0x4), which buffered O_SYNC writes set, while the real
// IOCB_DIRECT moved to bit 17.
const (
	iocbDirectLegacy = 1 << 2
	iocbDirectModern = 1 << 17
)

// iocbDirectBit returns the iocb.ki_flags bit that marks direct IO for the
// given kernel version. Testing the wrong bit inverts direct-vs-buffered
// attribution on >= 5.10: a buffered O_SYNC write (bit 2) is mislabelled
// direct, and a real O_DIRECT write (bit 17) is missed.
func iocbDirectBit(major, minor int) uint32 {
	if major > 5 || (major == 5 && minor >= 10) {
		return iocbDirectModern
	}
	return iocbDirectLegacy
}

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
	TGID            uint32
	PathInitialized uint32
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

type bpfScheduleDelay = abi.IotracingScheduleDelayEvent

// IsDirect reports whether the IO bypassed the page cache. directBit is the
// kernel-version-specific IOCB_DIRECT bit from iocbDirectBit. The Ino == 0
// branch covers direct IO seen on the block path, where user pages carry no
// address_space and therefore no inode.
func (r *bpfFilesystemIO) IsDirect(directBit uint32) bool {
	return r.Ino == 0 || r.Flags&directBit != 0
}

// PathName reconstructs the absolute file path from the BPF dentry walk.
// Empty when the BPF entry has no inode or no dentry path was captured.
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
	if len(names) == 0 {
		return ""
	}

	return "/" + strings.Join(names, "/")
}
