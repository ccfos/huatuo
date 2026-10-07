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
	"debug/elf"
	"encoding/binary"
	"fmt"
	versionpkg "go/version"
	"math"
)

// Heap profile layout contract (64-bit Go 1.18 through Go 1.26).
//
// Source audit: src/runtime/mprof.go at tags go1.18, go1.19, go1.20,
// go1.21.0, go1.22.0, go1.23.0, go1.24.0, go1.25.0, and go1.26.0.
// The audit covers bucket, memRecord, memRecordCycle, newBucket, stk, and mp.
// These releases share the byte layout below; individual patch releases and
// custom toolchains have not all been audited or tested. This is a private
// runtime layout, not a Go compatibility guarantee. Before extending the
// version range, audit these definitions and run real-process collection tests.
//
// Sources:
// https://github.com/golang/go/blob/go1.18/src/runtime/mprof.go
// https://github.com/golang/go/blob/go1.19/src/runtime/mprof.go
// https://github.com/golang/go/blob/go1.20/src/runtime/mprof.go
// https://github.com/golang/go/blob/go1.21.0/src/runtime/mprof.go
// https://github.com/golang/go/blob/go1.22.0/src/runtime/mprof.go
// https://github.com/golang/go/blob/go1.23.0/src/runtime/mprof.go
// https://github.com/golang/go/blob/go1.24.0/src/runtime/mprof.go
// https://github.com/golang/go/blob/go1.25.0/src/runtime/mprof.go
// https://github.com/golang/go/blob/go1.26.0/src/runtime/mprof.go
//
// Two links serve different purposes:
//
//	buckhash[index] --next--> bucket --next--> bucket
//	runtime.mbuckets ------> bucket --allnext--> bucket --> nil
//
// The hash chains locate profiles by type, stack, and allocation size. The
// mbuckets chain contains memory profiles; collection follows allnext, not next.
// New buckets are prepended. Existing headers and links are immutable, while
// counters continue changing. Reading the mbuckets variable yields the first
// bucket address, not the address of the variable itself.
//
// For a bucket at address B, all offsets below are bytes:
//
//	[B, B+48)              bucket header
//	[B+48, B+48+8*nstk)    nstk stack PCs (not a Go slice header)
//	[B+48+8*nstk, ...+128) memRecord
//
// Header fields (each 8 bytes):
//
//	offset   field     representation
//	     0   next      *bucket, hash-chain link
//	     8   allnext   *bucket, profile-list link
//	    16   typ       int, memory profile type is 1
//	    24   hash      uintptr
//	    32   size      uintptr, allocation size
//	    40   nstk      uintptr, actual number of stack PCs
//
// memRecord offsets, relative to B+48+8*nstk:
//
//	 0   active       memRecordCycle (32 bytes)
//	32   future[0]    memRecordCycle (32 bytes)
//	64   future[1]    memRecordCycle (32 bytes)
//	96   future[2]    memRecordCycle (32 bytes)
//
// Each cycle contains uintptr counters at offsets 0 (allocs), 8 (frees),
// 16 (alloc_bytes), and 24 (free_bytes). Active contains published cumulative
// statistics; future is a ring of three not-yet-published profile cycles.
// decodeCounters sums all four records before subtracting frees. External reads
// do not acquire runtime locks: updates or transfers from future to active can
// produce inconsistent observations. This is not an atomic MemProfile snapshot.
//
// Version differences that preserve the byte layout:
//   - Go 1.18 uses *bucket for mbuckets; Go 1.19+ uses atomic.UnsafePointer.
//     Its zero-size noCopy marker precedes the pointer; the value is still an
//     8-byte pointer at offset zero on the supported 64-bit targets.
//   - Go 1.20+ prefixes bucket with sys.NotInHeap, a zero-size marker that does
//     not move the following fields.
//   - Go 1.18-1.22 limits stacks to 32 PCs; Go 1.23-1.26 allows up to 1024.
//     Storage always uses actual nstk, so no version-specific padding is needed.
//
// newRuntimeLayout gates versions and rejects ELF32. Decoding uses the target
// ELF byte order. Layout compatibility does not imply stripped-symbol recovery
// support: that recovery currently depends on AMD64 instruction patterns.
const (
	minGoVersion = "go1.18"
	maxGoVersion = "go1.26"

	// bucketHeaderBytes covers the six 8-byte fields in runtime.bucket.
	// The variable-length PC array and profile record follow the header.
	bucketHeaderBytes = 48

	// programCounterBytes is the width of each uintptr PC in the target runtime.
	// Only 64-bit ELF targets are supported.
	programCounterBytes = 8

	// heapProfileRecordBytes covers runtime.memRecord on supported targets:
	// one active and three future cycles, each holding four 8-byte counters.
	heapProfileRecordBytes = 128
)

type runtimeLayout struct {
	byteOrder     binary.ByteOrder
	maxStackDepth uint64
}

type bucketHeader struct {
	raw [bucketHeaderBytes]byte
}

type bucketDescriptor struct {
	nextAddr   uint64
	stackAddr  uint64
	recordAddr uint64
	stackDepth int
}

// Version selection is the only owner of the supported runtime layout range.
func newRuntimeLayout(goVersion string, class elf.Class, order binary.ByteOrder) (runtimeLayout, error) {
	if class != elf.ELFCLASS64 {
		return runtimeLayout{}, fmt.Errorf("%w: ELF class %s; only ELFCLASS64 (64-bit) is supported", errUnsupportedRuntime, class)
	}
	languageVersion := versionpkg.Lang(goVersion)
	if languageVersion == "" || versionpkg.Compare(languageVersion, minGoVersion) < 0 || versionpkg.Compare(languageVersion, maxGoVersion) > 0 {
		return runtimeLayout{}, fmt.Errorf("%w: unsupported Go runtime version %q: supported range is %s-%s", errUnsupportedRuntime, goVersion, minGoVersion, maxGoVersion)
	}

	// Match the target runtime's stack bound when validating bucket.nstk:
	// Go 1.23 raised the maximum profile stack depth from 32 to 1024 PCs.
	// https://github.com/golang/go/blob/go1.22.0/src/runtime/mprof.go
	// https://github.com/golang/go/blob/go1.23.0/src/runtime/mprof.go
	depth := uint64(32)
	if versionpkg.Compare(languageVersion, "go1.23") >= 0 {
		depth = 1024
	}
	return runtimeLayout{byteOrder: order, maxStackDepth: depth}, nil
}

// decodeBucketHeader validates remote bytes and the entire record range before the
// scanner can schedule reads. The caller must first read the complete header.
func (l runtimeLayout) decodeBucketHeader(addr uint64, header *bucketHeader) (bucketDescriptor, error) {
	if typ := l.byteOrder.Uint64(header.raw[16:24]); typ != 1 {
		return bucketDescriptor{}, fmt.Errorf("mbucket profile type %d is not memory profile type 1", typ)
	}
	depth := l.byteOrder.Uint64(header.raw[40:48])
	if depth > l.maxStackDepth {
		return bucketDescriptor{}, fmt.Errorf("mbucket stack depth %d exceeds limit %d", depth, l.maxStackDepth)
	}
	// Depth is bounded above, so the remaining risk is the remote base address.
	lastOffset := uint64(bucketHeaderBytes) + depth*programCounterBytes + heapProfileRecordBytes - 1
	if addr > math.MaxUint64-lastOffset {
		return bucketDescriptor{}, fmt.Errorf("mbucket stack or record range overflows address space")
	}
	stackAddr := addr + bucketHeaderBytes
	return bucketDescriptor{
		nextAddr:   l.byteOrder.Uint64(header.raw[8:16]),
		stackAddr:  stackAddr,
		recordAddr: stackAddr + depth*programCounterBytes,
		stackDepth: int(depth),
	}, nil
}

// decodeCounters returns in-use counts. Remote reads can observe inconsistent
// counters, so negative differences are clamped to zero.
func (l runtimeLayout) decodeCounters(raw *[heapProfileRecordBytes]byte) (objects, bytes uint64) {
	var allocObjects, freeObjects, allocBytes, freeBytes uint64
	// Match runtime.memRecordCycle.add on supported 64-bit targets:
	// unsigned sums wrap on overflow.
	// https://github.com/golang/go/blob/go1.23.0/src/runtime/mprof.go#L154-L159
	for base := 0; base < heapProfileRecordBytes; base += 32 {
		allocObjects += l.byteOrder.Uint64(raw[base : base+8])
		freeObjects += l.byteOrder.Uint64(raw[base+8 : base+16])
		allocBytes += l.byteOrder.Uint64(raw[base+16 : base+24])
		freeBytes += l.byteOrder.Uint64(raw[base+24 : base+32])
	}

	return allocObjects - min(allocObjects, freeObjects), allocBytes - min(allocBytes, freeBytes)
}
