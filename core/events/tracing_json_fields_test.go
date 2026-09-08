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
	"time"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/metric"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/pkg/types"
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
			value:    RasTracingData{Device: "cpu"},
			expected: []string{"dev"},
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

func TestReclaimUnresolvedOutputCooldown(t *testing.T) {
	attr, err := newMemoryReclaim()
	require.NoError(t, err)
	require.NotZero(t, attr.Flag&tracing.FlagMetric)
	c := attr.TracingData.(*memoryReclaimTracing)
	now := time.Unix(100, 0)
	require.True(t, c.allowReclaimOutput(nil, now))
	for range 100 {
		require.False(t, c.allowReclaimOutput(nil, now), "unresolved flood exceeded output budget")
	}
	require.False(t, c.allowReclaimOutput(nil, now.Add(time.Second-time.Nanosecond)))
	deadline := c.nextUnresolved
	for range 100 {
		require.True(t, c.allowReclaimOutput(&pod.Container{ID: "resolved"}, now))
	}
	require.Equal(t, deadline, c.nextUnresolved, "resolved events consumed or refilled host budget")
	metrics, err := c.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.Equal(t, "unresolved_suppressed_total", metrics[0].Name())
	assert.Equal(t, metric.MetricTypeCounter, metrics[0].Type())
	assert.Equal(t, float64(101), metrics[0].Value)
	require.True(t, c.allowReclaimOutput(nil, now.Add(time.Second)), "exact cooldown boundary must recover")
	require.False(t, c.allowReclaimOutput(nil, now.Add(time.Second)), "cooldown must begin before saving")
	require.True(t, c.allowReclaimOutput(nil, now.Add(time.Hour)))
	require.False(t, c.allowReclaimOutput(nil, now.Add(time.Hour)), "idle time must not accumulate burst credits")
	metrics, err = c.Update()
	require.NoError(t, err)
	assert.Equal(t, float64(103), metrics[0].Value, "recovery must not reset suppression count")
}

func TestReclaimSuppressionConcurrentScrapes(t *testing.T) {
	c := &memoryReclaimTracing{}
	now := time.Unix(100, 0)
	require.True(t, c.allowReclaimOutput(nil, now))
	start, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		<-start
		for range 1000 {
			c.allowReclaimOutput(nil, now)
		}
	}()
	close(start)
	var previous float64
	for range 1000 {
		metrics, err := c.Update()
		require.NoError(t, err)
		require.GreaterOrEqual(t, metrics[0].Value, previous)
		previous = metrics[0].Value
	}
	<-done
	metrics, err := c.Update()
	require.NoError(t, err)
	assert.Equal(t, float64(1000), metrics[0].Value)
}

func BenchmarkReclaimOutputCooldown(b *testing.B) {
	for _, mode := range []string{"resolved", "allowed", "suppressed"} {
		b.Run(mode, func(b *testing.B) {
			c := &memoryReclaimTracing{}
			now := time.Unix(100, 0)
			var container *pod.Container
			if mode == "resolved" {
				container = &pod.Container{ID: "resolved"}
			}
			if mode == "suppressed" {
				c.nextUnresolved = now.Add(time.Hour)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if mode == "allowed" {
					now = now.Add(time.Second)
				}
				if c.allowReclaimOutput(container, now) != (mode != "suppressed") {
					b.Fatal("unexpected output decision")
				}
			}
		})
	}
}
