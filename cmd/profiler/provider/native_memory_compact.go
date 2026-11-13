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

package provider

import (
	"fmt"
	"slices"

	"github.com/ccfos/huatuo/internal/profiling/window"

	"github.com/ccfos/huatuo/internal/bpf/abi"
)

type retainedMemorySample struct {
	source     abi.ProfilerEventBase
	valueBytes int64
}

// compactRetainedEvents combines only identical raw sources within one frozen
// read. Convert each input before summing so scaling and overflow validation
// remain per-event. Original perf accounting is performed before compaction.
func compactRetainedEvents(events []any, convert func(int64) (int64, error)) ([]retainedMemorySample, error) {
	indices := make(map[abi.ProfilerEventBase]int)
	var result []retainedMemorySample
	for _, record := range events {
		event, ok := record.(*abi.ProfilerEventBase)
		if !ok || event == nil {
			return nil, fmt.Errorf("unexpected retained perf event %T", record)
		}

		value, err := convert(event.Value)
		if err != nil {
			return nil, err
		}

		key := *event
		key.Value = 0
		if index, ok := indices[key]; ok {
			previous := &result[index]
			sum, err := window.AddValues(previous.valueBytes, value)
			if err != nil {
				return nil, fmt.Errorf("compact retained events: %w", err)
			}

			previous.valueBytes = sum
		} else {
			indices[key] = len(result)
			// Double capacity for unique sources without reserving the raw batch size
			// when thousands of events collapse to only a few samples.
			if len(result) == cap(result) {
				result = slices.Grow(result, min(max(8, cap(result)), len(events)-len(result)))
			}
			result = append(result, retainedMemorySample{
				source:     key,
				valueBytes: value,
			})
		}
	}

	return result, nil
}
