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
	"runtime"
	"syscall"
	"time"
)

func init() {
	// init runs on the startup thread; keep main on the process leader so
	// --pid without --thread-group cannot accidentally sample a worker.
	runtime.LockOSThread()
}

func main() {
	runtime.GOMAXPROCS(2)
	ready := make(chan int, 2)
	go goCPUWorker(1, ready)
	go goCPUWorker(2, ready)
	fmt.Printf("ready main=%d worker=%d worker=%d\n", syscall.Gettid(), <-ready, <-ready)
	// Bound orphaned workloads if the test runner is interrupted.
	time.Sleep(120 * time.Second)
}

//go:noinline
func goCPUWorker(seed uint64, ready chan<- int) {
	runtime.LockOSThread()
	ready <- syscall.Gettid()
	for {
		seed = goCPUMiddle(seed)
	}
}

//go:noinline
func goCPUMiddle(seed uint64) uint64 {
	return goCPUHot(seed)
}

//go:noinline
func goCPUHot(seed uint64) uint64 {
	// Keep a frame pointer in optimized builds: a frameless leaf would make
	// the kernel frame-pointer unwinder skip goCPUMiddle. Yield only between
	// batches so most samples still land on the allocation-free hot loop.
	runtime.Gosched()
	for i := 0; i < 1<<22; i++ {
		seed = seed*1664525 + 1013904223
		seed ^= seed >> 13
	}
	return seed
}
