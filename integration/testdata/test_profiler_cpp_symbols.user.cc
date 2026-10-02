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

#include <time.h>

namespace huatuo::symbol_fixture {

static volatile unsigned long sink;

template <typename T>
__attribute__((noinline)) void consume(T iterations) {
	T accumulator = 0;
	for (T index = 0; index < iterations; ++index) {
		accumulator += index * 2654435761UL;
	}
	sink += accumulator;
}

}

static double monotonic_seconds() {
	struct timespec timestamp;
	clock_gettime(CLOCK_MONOTONIC, &timestamp);
	return static_cast<double>(timestamp.tv_sec) +
	       static_cast<double>(timestamp.tv_nsec) / 1e9;
}

int main() {
	constexpr double duration = 30.0;
	constexpr unsigned long iterations_per_call = 200000UL;
	const double deadline = monotonic_seconds() + duration;

	while (monotonic_seconds() < deadline) {
		huatuo::symbol_fixture::consume(iterations_per_call);
	}

	return 0;
}
