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
	"os"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/pcapfilter"
)

func loadDropwatchBPFWithFilter(bpfPath, filterExpr string, devMode uint32, maxEventsPerSecond uint64) (bpf.BPF, error) {
	bpfBytes, err := os.ReadFile(bpfPath)
	if err != nil {
		return nil, fmt.Errorf("read bpf object: %w", err)
	}

	bpfName := fmt.Sprintf("dropwatch_%d.o", time.Now().UnixNano())

	return pcapfilter.Load(
		bpfName,
		bpfBytes,
		filterExpr,
		withRateLimitConstants(map[string]any{"filter_dev_mode": devMode}, maxEventsPerSecond),
	)
}
