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
	"io"
	"os"
	"strings"
	"sync"
)

var (
	kprobeOnce   sync.Mutex
	kprobeCache  map[string]struct{}
	kprobeCached bool

	// Tracefs has its own mount point on current kernels, while older systems
	// commonly expose the same file through debugfs.  Check both layouts.
	kprobeFunctionFiles = []string{
		"/sys/kernel/tracing/available_filter_functions",
		"/sys/kernel/debug/tracing/available_filter_functions",
	}
)

func scanKprobeFunctions(r io.Reader, cache map[string]struct{}) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		cache[strings.Fields(line)[0]] = struct{}{}
	}
	return scanner.Err()
}

// loadKprobeFunctions reads the available function list and populates
// kprobeCache. On success kprobeCached is set so subsequent calls skip the
// file read; if neither tracefs nor debugfs is readable, the next call retries.
func loadKprobeFunctions() {
	cache := make(map[string]struct{})
	readOK := false
	for _, path := range kprobeFunctionFiles {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		err = scanKprobeFunctions(file, cache)
		_ = file.Close()
		if err == nil {
			readOK = true
		}
	}

	if readOK {
		kprobeCache = cache
		kprobeCached = true
	}
}

// HasKprobeFunction returns whether the given symbol is reported as
// attachable in the kernel's available_filter_functions list.
func HasKprobeFunction(name string) bool {
	kprobeOnce.Lock()
	defer kprobeOnce.Unlock()
	if !kprobeCached {
		loadKprobeFunctions()
	}

	_, ok := kprobeCache[name]
	return ok
}
