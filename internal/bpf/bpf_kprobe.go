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

package bpf

import (
	"bufio"
	"os"
	"strings"
	"sync"
)

var (
	kprobeOnce   sync.Mutex
	kprobeCache  map[string]struct{}
	kprobeCached bool
)

// loadKprobeFunctions reads /sys/kernel/debug/tracing/available_filter_functions
// and populates kprobeCache. On success kprobeCached is set so subsequent
// calls skip the file read; on failure the cache remains unset and the next
// call retries.
func loadKprobeFunctions() {
	file, err := os.Open("/sys/kernel/debug/tracing/available_filter_functions")
	if err != nil {
		return
	}
	defer file.Close()

	cache := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		cache[strings.Fields(line)[0]] = struct{}{}
	}

	if err := scanner.Err(); err != nil {
		return
	}

	kprobeCache = cache
	kprobeCached = true
}

// HasKprobeFunction returns whether the given symbol is reported as
// attachable in the kernel's available_filter_functions list.
func HasKprobeFunction(name string) bool {
	kprobeOnce.Lock()
	if !kprobeCached {
		loadKprobeFunctions()
	}
	kprobeOnce.Unlock()

	_, ok := kprobeCache[name]
	return ok
}
