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

package events

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"huatuo-bamai/pkg/types"
)

func TestEventJSONFieldNames(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		expected []string
	}{
		{
			name: "scheduler tick",
			value: SchedTickTracingData{
				TickIntervalNS:          20,
				TickIntervalThresholdNS: 10,
			},
			expected: []string{"tick_interval_ns", "tick_interval_threshold_ns"},
		},
		{
			name: "memory reclaim",
			value: MemoryReclaimTracingData{
				PID:               10,
				TID:               11,
				ReclaimDurationNS: 20,
			},
			expected: []string{"pid", "tid", "reclaim_duration_ns"},
		},
		{
			name:     "hung task",
			value:    HungTaskTracerData{TID: 11},
			expected: []string{"tid"},
		},
		{
			name:     "oom actor",
			value:    OOMActor{PID: 10},
			expected: []string{"pid"},
		},
		{
			name: "network receive latency",
			value: NetTracingData{
				LatencyMS:          20,
				LatencyThresholdMS: 10,
				PacketLenBytes:     64,
			},
			expected: []string{
				"latency_stage",
				"latency_ms",
				"latency_threshold_ms",
				"packet_len_bytes",
				"net_namespace_inum",
				"net_namespace_cookie",
			},
		},
		{
			name:     "dropwatch",
			value:    types.DropWatchTracing{PacketLenBytes: 64},
			expected: []string{"packet_len_bytes"},
		},
		{
			name:     "ras",
			value:    RasTracingData{ObservedTimestamp: "2026-08-20T00:00:00Z"},
			expected: []string{"observed_timestamp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := json.Marshal(tt.value)
			require.NoError(t, err)

			fields := map[string]json.RawMessage{}
			require.NoError(t, json.Unmarshal(encoded, &fields))
			for _, name := range tt.expected {
				assert.Contains(t, fields, name)
			}
		})
	}
}
