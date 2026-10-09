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
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		panic("usage: go-snapshot <memory-profile-rate>")
	}

	rate, err := strconv.Atoi(os.Args[1])
	if err != nil || (rate != 0 && rate != 1) {
		panic("memory profile rate must be 0 or 1")
	}

	runtime.MemProfileRate = rate
	// Automatic GC can attribute its bookkeeping objects to the allocating stack.
	// Explicit cycles below publish the workload without introducing that noise.
	debug.SetGCPercent(-1)

	// Let the test observe the container before charging its memory.
	waitForCommand('a')
	primary := allocatePrimary()
	secondary := allocateSecondary()
	allocateReleased()

	// Publish frees as well as allocations before the shell can trigger capture.
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
	// Opt-in churn exercises capture while the target allocates, frees and runs GC.
	if os.Getenv("MEMSNAPSHOT_CHURN") == "1" {
		go func() {
			for {
				block := allocateBlock(2 << 20)
				runtime.KeepAlive(block)
				runtime.GC()
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	fmt.Println("ready")
	waitForCommand('p')

	// Anonymous mmap charges real memory without adding Go heap profile samples.
	pressure, err := syscall.Mmap(
		-1,
		0,
		64<<20,
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_PRIVATE|syscall.MAP_ANON,
	)
	if err != nil {
		panic(fmt.Errorf("map pressure memory: %w", err))
	}
	touchPages(pressure)
	fmt.Println("pressure ready")
	waitForCommand('q')
	if err := syscall.Munmap(pressure); err != nil {
		panic(fmt.Errorf("unmap pressure memory: %w", err))
	}

	runtime.KeepAlive(primary)
	runtime.KeepAlive(secondary)
}

func waitForCommand(expected byte) {
	var command [1]byte
	if _, err := io.ReadFull(os.Stdin, command[:]); err != nil {
		panic(fmt.Errorf("read command %q: %w", expected, err))
	}
	if command[0] != expected {
		panic(fmt.Sprintf("command = %q, want %q", command[0], expected))
	}
}

// Keep the same allocation PC for both sizes so runtime buckets must be merged.
//
//go:noinline
func allocatePrimary() [6][]byte {
	var blocks [6][]byte
	for i := range blocks {
		size := 2 << 20
		if i >= 4 {
			size = 4 << 20
		}
		if os.Getenv("MEMSNAPSHOT_VARIANT") == "small" {
			size = 1 << 20
		}
		blocks[i] = allocateBlock(size)
	}

	return blocks
}

//go:noinline
func allocateSecondary() [3][]byte {
	var blocks [3][]byte
	for i := range blocks {
		blocks[i] = allocateBlock(2 << 20)
	}

	return blocks
}

//go:noinline
func allocateReleased() {
	block := allocateBlock(4 << 20)
	runtime.KeepAlive(block)
}

// The shared leaf makes caller identity part of the aggregation contract.
//
//go:noinline
func allocateBlock(size int) []byte {
	block := make([]byte, size)
	touchPages(block)
	return block
}

func touchPages(memory []byte) {
	for i := 0; i < len(memory); i += os.Getpagesize() {
		memory[i] = 1
	}
}
