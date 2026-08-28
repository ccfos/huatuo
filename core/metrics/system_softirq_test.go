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
	"testing"
)

func TestAppendSoftirqMetricsSkipsOfflineCPUs(t *testing.T) {
	latencies := make([]softirqLatencyData, 3)
	latencies[0].LatencyCounts[0] = 10
	latencies[1].LatencyCounts[0] = 20
	latencies[2].LatencyCounts[0] = 30
	online := map[int]struct{}{0: {}, 2: {}}

	metrics := appendSoftirqMetrics(nil, softirqNetRx, latencies, online)
	if len(metrics) != 8 {
		t.Fatalf("appendSoftirqMetrics() returned %d metrics, want 8", len(metrics))
	}
	seen := map[string]bool{}
	for _, data := range metrics {
		labels := data.Labels()
		seen[labels["cpuid"]] = true
		if labels["type"] != "NET_RX" {
			t.Errorf("type label = %q, want NET_RX", labels["type"])
		}
	}
	if seen["1"] || !seen["0"] || !seen["2"] {
		t.Errorf("CPU labels = %v, want only 0 and 2", seen)
	}
}
