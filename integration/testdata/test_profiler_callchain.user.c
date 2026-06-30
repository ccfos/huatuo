// Copyright 2026 The HuaTuo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Deterministic main->f1->f2->f3 call chain for native CPU profiler testing.
// The hot loop in f3 ensures 99 Hz sampling captures the full chain.
// Compile with -O0 -g -fno-inline -fno-omit-frame-pointer; the attributes
// below are a second line of defense if flags are accidentally changed.

#include <stdlib.h>
#include <time.h>
#include <unistd.h>

// Prevent merge/specialization so the stack walker sees the source-level chain.
#define KEEP_FRAME __attribute__((noinline, noclone))

// Defeats dead-store elimination; without it -O2 would collapse f3 to a no-op
// and the profiler would sample only main / kernel frames.
static volatile unsigned long sink;

static KEEP_FRAME void f3(unsigned long iters) {
	unsigned long acc = 0;
	for (unsigned long i = 0; i < iters; i++) {
		acc += i * 2654435761UL;
	}
	sink += acc;
}

static KEEP_FRAME void f2(unsigned long iters) {
	f3(iters);
}

static KEEP_FRAME void f1(unsigned long iters) {
	f2(iters);
}

// CLOCK_MONOTONIC avoids wall-clock jumps that could cut the run short or
// hang it past the test timeout.
static double monotonic_seconds(void) {
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (double)ts.tv_sec + (double)ts.tv_nsec / 1e9;
}

int main(void) {
	const double duration = 30.0;
	const unsigned long iters_per_call = 200000UL;
	const double deadline = monotonic_seconds() + duration;

	while (monotonic_seconds() < deadline) {
		f1(iters_per_call);
	}

	return 0;
}
