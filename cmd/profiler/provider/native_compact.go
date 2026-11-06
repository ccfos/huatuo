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
	"math"

	"github.com/ccfos/huatuo/internal/bpf/abi"
)

// compactRetainedEvents combines only identical raw sources within one frozen
// read. Convert each input before summing so scaling and overflow validation
// remain per-event. Original perf accounting is performed before compaction.
func compactRetainedEvents(events []any, convert func(int64) (int64, error)) ([]any, error) {
	indices := make(map[abi.ProfilerEventBase]int)
	result := make([]any, 0, len(events))
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
			previous := result[index].(*abi.ProfilerEventBase)
			if value > 0 && previous.Value > math.MaxInt64-value || value < 0 && previous.Value < math.MinInt64-value {
				return nil, fmt.Errorf("retained event value overflows int64")
			}
			previous.Value += value
		} else {
			entry := key
			entry.Value = value
			indices[key] = len(result)
			result = append(result, &entry)
		}
	}
	return result, nil
}
