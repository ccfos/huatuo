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
	"fmt"
	"os"
	"runtime"
	"testing"
	"unsafe"
)

func BenchmarkScanWindows32MiB(b *testing.B) {
	data := make([]byte, maxSampleBytes)
	for i := 0; i < len(data); i += 4096 {
		data[i] = 1
	}
	address := uint64(uintptr(unsafe.Pointer(&data[0])))
	regions := []region{{bottom: address, top: address + uint64(len(data))}}
	memory := processMemory{pid: os.Getpid(), ctx: context.Background()}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var observed uint64
		reason := scanWindows(memory, regions, uint64(i), maxSampleBytes, windowBytes, func(batch []sampleWindow) bool {
			for _, window := range batch {
				observed += uint64(len(window.raw))
			}
			return true
		})
		if reason != "" || observed != maxSampleBytes {
			b.Fatalf("observed=%d reason=%s", observed, reason)
		}
	}
	runtime.KeepAlive(data)
}

func TestScanWindowsBatches(t *testing.T) {
	const size = 4096
	const windows = maxReadIOVs*2 + 1
	data := mapTestMemory(t, (windows*size+os.Getpagesize()-1)/os.Getpagesize())
	data = data[:windows*size]
	for i := 0; i < windows; i++ {
		binary.LittleEndian.PutUint64(data[i*size:], uint64(i))
	}
	address := uint64(uintptr(unsafe.Pointer(&data[0])))
	regions := []region{{bottom: address, top: address + uint64(len(data))}}
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			seen := make(map[uint64]bool)
			calls := 0
			reason := scanWindows(processMemory{pid: os.Getpid(), ctx: t.Context()}, regions, 0, uint64(len(data)), size, func(batch []sampleWindow) bool {
				calls++
				for _, window := range batch {
					index := (window.start - address) / size
					if seen[index] || len(window.raw) != size || binary.LittleEndian.Uint64(window.raw) != index {
						t.Fatalf("duplicate or stale window %d", index)
					}
					seen[index] = true
				}
				return !stop
			})
			if stop {
				if calls != 1 || reason == "" {
					t.Fatalf("stop: calls=%d reason=%s", calls, reason)
				}
			} else if len(seen) != windows || reason != "" {
				t.Fatalf("scan: windows=%d reason=%s", len(seen), reason)
			}
		})
	}
}
