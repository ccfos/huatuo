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

package collector

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"huatuo-bamai/internal/qdisc"
	"huatuo-bamai/pkg/metric"
)

func TestQdiscCollectorUpdate(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.Qdisc.DeviceIncluded = `^eth[01]$`
	testConfig.Qdisc.DeviceExcluded = `^eth1$`
	Set(testConfig)

	attr, err := newQdiscCollector()
	if err != nil {
		t.Fatalf("create qdisc collector: %v", err)
	}
	collector, ok := attr.TracingData.(*qdiscCollector)
	if !ok {
		t.Fatalf("tracing data type = %T, want *qdiscCollector", attr.TracingData)
	}
	collector.readStats = func() ([]qdisc.Stats, error) {
		return []qdisc.Stats{
			{
				Netdev:       "eth0",
				Parent:       math.MaxUint32,
				Kind:         "fq_codel",
				Bytes:        1,
				Packets:      2,
				Drops:        3,
				Requeues:     4,
				Overlimits:   5,
				QueueLength:  6,
				BacklogBytes: 7,
			},
			{Netdev: "eth1", Parent: math.MaxUint32, Kind: "fq"},
			{Netdev: "eth0", Parent: 1, Kind: "fq"},
			{Netdev: "eth0", Parent: math.MaxUint32, Kind: "noqueue"},
			{Netdev: "lo", Parent: math.MaxUint32, Kind: "fq"},
		}, nil
	}

	metrics, err := collector.Update()
	if err != nil {
		t.Fatalf("update qdisc metrics: %v", err)
	}
	want := []qdiscMetric{
		{Name: "bytes_total", Value: 1, Device: "eth0", Kind: "fq_codel"},
		{Name: "packets_total", Value: 2, Device: "eth0", Kind: "fq_codel"},
		{Name: "drops_total", Value: 3, Device: "eth0", Kind: "fq_codel"},
		{Name: "requeues_total", Value: 4, Device: "eth0", Kind: "fq_codel"},
		{Name: "overlimits_total", Value: 5, Device: "eth0", Kind: "fq_codel"},
		{Name: "current_queue_length", Value: 6, Device: "eth0", Kind: "fq_codel"},
		{Name: "backlog", Value: 7, Device: "eth0", Kind: "fq_codel"},
	}
	if diff := cmp.Diff(want, summarizeQdiscMetrics(metrics)); diff != "" {
		t.Fatalf("qdisc metrics mismatch (-want +got):\n%s", diff)
	}
}

func TestQdiscCollectorUpdateReadError(t *testing.T) {
	wantErr := errors.New("netlink unavailable")
	collector := &qdiscCollector{
		readStats: func() ([]qdisc.Stats, error) { return nil, wantErr },
	}

	_, err := collector.Update()
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapping %v", err, wantErr)
	}
}

func TestNewQdiscCollectorRejectsInvalidDeviceFilter(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() { Set(originalConfig) })
	testConfig := &Config{}
	testConfig.Qdisc.DeviceIncluded = "["
	Set(testConfig)

	_, err := newQdiscCollector()
	if err == nil || !strings.Contains(err.Error(), "qdisc device filter") {
		t.Fatalf("error = %v, want invalid qdisc device filter", err)
	}
}

func BenchmarkQdiscCollectorUpdate(b *testing.B) {
	stats := make([]qdisc.Stats, 128)
	for i := range stats {
		stats[i] = qdisc.Stats{
			Netdev:       "eth0",
			Parent:       math.MaxUint32,
			Kind:         "fq_codel",
			Bytes:        1_024,
			Packets:      128,
			Drops:        1,
			QueueLength:  2,
			BacklogBytes: 4_096,
		}
	}
	collector := &qdiscCollector{
		readStats: func() ([]qdisc.Stats, error) { return stats, nil },
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := collector.Update(); err != nil {
			b.Fatalf("update qdisc metrics: %v", err)
		}
	}
}

type qdiscMetric struct {
	Name   string
	Value  float64
	Device string
	Kind   string
}

func summarizeQdiscMetrics(metrics []*metric.Data) []qdiscMetric {
	summary := make([]qdiscMetric, 0, len(metrics))
	for _, data := range metrics {
		labels := data.Labels()
		summary = append(summary, qdiscMetric{
			Name:   data.Name(),
			Value:  data.Value,
			Device: labels["device"],
			Kind:   labels["kind"],
		})
	}
	return summary
}
