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

package main

import (
	"syscall"
	"time"
)

var sink uint64

//go:noinline
func hotLoop() {
	for i := uint64(0); i < 1000000; i++ {
		sink += i * 2654435761
	}
}

func main() {
	// Keep live mappings so retained-memory snapshots have a stable target.
	var retained [16][]byte
	next := 0
	for {
		hotLoop()
		data, err := syscall.Mmap(
			-1,
			0,
			1024*1024,
			syscall.PROT_READ|syscall.PROT_WRITE,
			syscall.MAP_PRIVATE|syscall.MAP_ANON,
		)
		if err != nil {
			panic(err)
		}
		for i := 0; i < len(data); i += 4096 {
			data[i] = 1
		}
		time.Sleep(time.Millisecond)
		if retained[next] != nil {
			if err := syscall.Munmap(retained[next]); err != nil {
				panic(err)
			}
		}
		retained[next] = data
		next = (next + 1) % len(retained)
	}
}
