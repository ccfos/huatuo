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
	"testing"
	"time"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/pkg/metric"
	"huatuo-bamai/pkg/tracing"
)

func TestNewNetTxLatency(t *testing.T) {
	attr, err := newNetTxLatency()
	if err != nil {
		t.Fatalf("newNetTxLatency() error = %v", err)
	}
	if attr.Flag != tracing.FlagTracing|tracing.FlagMetric {
		t.Fatalf("newNetTxLatency().Flag = %d", attr.Flag)
	}
	if attr.Interval != 10 {
		t.Fatalf("newNetTxLatency().Interval = %d, want 10", attr.Interval)
	}
	if _, ok := attr.TracingData.(*netTxLatencyTracing); !ok {
		t.Fatalf("newNetTxLatency().TracingData type = %T", attr.TracingData)
	}
}

func TestNetTxLatencyMetrics(t *testing.T) {
	tracer := &netTxLatencyTracing{}
	tracer.recordMetric(1, uint64(2*time.Second))
	tracer.recordMetric(1, uint64(3*time.Second))

	data, err := tracer.Update()
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if len(data) != netTxLatencyStageCount*3 {
		t.Fatalf("Update() returned %d metrics, want %d", len(data), netTxLatencyStageCount*3)
	}

	assertMetricValue(t, data, "events_total", "TX_STAGE_QDISC_DEV", 2)
	assertMetricValue(t, data, "seconds_total", "TX_STAGE_QDISC_DEV", 5)
	assertMetricValue(t, data, "last_seconds", "TX_STAGE_QDISC_DEV", 3)
	assertMetricValue(t, data, "events_total", "TX_STAGE_SENDMSG_QDISC", 0)
}

func assertMetricValue(t *testing.T, data []*metric.Data, name, stage string, want float64) {
	t.Helper()
	for _, item := range data {
		if item.Name() == name && item.Labels()["stage"] == stage {
			if item.Value != want {
				t.Fatalf("metric %s{%s} = %v, want %v", name, stage, item.Value, want)
			}
			return
		}
	}
	t.Fatalf("metric %s{%s} not found", name, stage)
}

func TestNewNetTxTracingData(t *testing.T) {
	var event abi.NetRXLatencyEvent
	copy(event.Comm[:], "sender")
	copy(event.NetdevName[:], "eth0")
	event.TGIDPID = uint64(42) << 32
	event.LatencyStage = 2
	event.LatencyNS = uint64(2500 * time.Microsecond)
	event.PacketLenBytes = 1500
	event.NetNamespaceInum = 123
	event.NetNamespaceCookie = 456

	data := newNetTxTracingData(&event, 1)
	if data.Comm != "sender" || data.PID != 42 {
		t.Fatalf("process data = %q/%d, want sender/42", data.Comm, data.PID)
	}
	if data.LatencyStage != "TX_STAGE_DEV_NIC" || data.LatencyMS != 2.5 {
		t.Fatalf("latency data = %q/%v, want TX_STAGE_DEV_NIC/2.5", data.LatencyStage, data.LatencyMS)
	}
	if data.NetdevName != "eth0" || data.PacketLenBytes != 1500 {
		t.Fatalf("packet data = %q/%d, want eth0/1500", data.NetdevName, data.PacketLenBytes)
	}
	if data.NetNamespaceInum != 123 || data.NetNamespaceCookie != 456 {
		t.Fatalf("namespace data = %d/%d, want 123/456", data.NetNamespaceInum, data.NetNamespaceCookie)
	}
}

func BenchmarkNewNetTxTracingData(b *testing.B) {
	event := abi.NetRXLatencyEvent{
		LatencyStage:   1,
		LatencyNS:      uint64(2 * time.Millisecond),
		PacketLenBytes: 1500,
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = newNetTxTracingData(&event, 10)
	}
}
