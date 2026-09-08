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
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"

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

type hungTaskAttachStub struct {
	bpf.BPF
	attachErr, closeErr error
	closed              int
}

func (s *hungTaskAttachStub) AttachAndEventPipe(context.Context, string, uint32) (bpf.PerfEventReader, error) {
	return nil, s.attachErr
}
func (s *hungTaskAttachStub) Close() error { s.closed++; return s.closeErr }
func TestHungTaskHostFallback(t *testing.T) {
	for _, stage := range []string{"load", "attach", "host", "close"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("container unavailable")
			hostFailure := errors.New("host unavailable")
			closeFailure := errors.New("close failed")
			first, last := &hungTaskAttachStub{}, &hungTaskAttachStub{}
			calls := []bool{}
			obj, _, err := startHungTaskBPF(context.Background(), "fixture", func(_ string, containers bool) (bpf.BPF, error) {
				calls = append(calls, containers)
				if !containers {
					if stage == "host" {
						return nil, hostFailure
					}
					return last, nil
				}
				if stage == "load" || stage == "host" {
					return nil, failure
				}
				first.attachErr = failure
				if stage == "close" {
					first.closeErr = closeFailure
				}
				return first, nil
			})
			if stage == "host" || stage == "close" {
				if obj != nil || !errors.Is(err, failure) {
					t.Fatalf("obj=%v err=%v", obj, err)
				}
				expected := hostFailure
				if stage == "close" {
					expected = closeFailure
				}
				if !errors.Is(err, expected) {
					t.Fatalf("missing error %v: %v", expected, err)
				}
			} else if err != nil || obj != last {
				t.Fatalf("fallback obj=%v err=%v", obj, err)
			}
			want := 2
			if stage == "close" {
				want = 1
			}
			if len(calls) != want || !calls[0] || (want == 2 && calls[1]) {
				t.Fatalf("load modes=%v", calls)
			}
			if stage == "attach" || stage == "close" {
				if first.closed != 1 {
					t.Fatal("failed object not closed")
				}
			}
		})
	}
}

func TestHungTaskContainerAttribution(t *testing.T) {
	nearest := &pod.Container{ID: "nearest", CgroupPath: "/nearest"}
	ancestor := &pod.Container{ID: "ancestor", CgroupPath: "/ancestor"}
	containers := map[string]*pod.Container{"nearest": nearest, "ancestor": ancestor, "invalid": {CgroupPath: "relative"}, "nil": nil}
	event := abi.HungtaskEvent{CgroupCount: 2}
	event.CgroupIds[0] = 42
	event.CgroupIds[1] = 43
	for _, tc := range []struct {
		name            string
		missing, failed bool
		want            *pod.Container
	}{
		{name: "nearest ancestor wins", want: nearest},
		{name: "missing nearest retains ancestor", missing: true, want: ancestor},
		{name: "resolver failure retains ancestor", failed: true, want: ancestor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hungTaskContainer(&event, containers, func(root string) (uint64, error) {
				if root == "/ancestor" {
					return 43, nil
				}
				if root != "/nearest" {
					t.Fatalf("invalid path resolved: %s", root)
				}
				if tc.failed {
					return 0, errors.New("unavailable")
				}
				if tc.missing {
					return 0, nil
				}
				return 42, nil
			})
			if got != tc.want {
				t.Fatalf("container=%v want=%v", got, tc.want)
			}
		})
	}
	for _, count := range []uint32{0, uint32(len(event.CgroupIds)) + 1} {
		event.CgroupCount = count
		if got := hungTaskContainer(&event, containers, func(string) (uint64, error) { t.Fatal("invalid event resolved"); return 0, nil }); got != nil {
			t.Fatal("invalid event attributed")
		}
	}
}
