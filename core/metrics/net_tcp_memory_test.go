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
	"io/fs"
	"testing"

	"huatuo-bamai/pkg/metric"
)

func TestTcpMemoryUpdateReportsUsagePercent(t *testing.T) {
	testcases := []struct {
		name        string
		stats       tcpMemoryStat
		wantPercent float64
	}{
		{
			name:        "quarter of the limit",
			stats:       tcpMemoryStat{memoryPages: 500, memoryLimit: 2000},
			wantPercent: 25,
		},
		{
			name:        "limit reached",
			stats:       tcpMemoryStat{memoryPages: 2000, memoryLimit: 2000},
			wantPercent: 100,
		},
		{
			name:        "no limit reported",
			stats:       tcpMemoryStat{memoryPages: 500, memoryLimit: 0},
			wantPercent: 0,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			c := &tcpMemory{parseStats: func() (*tcpMemoryStat, error) {
				stats := tc.stats
				return &stats, nil
			}}

			metrics, err := c.Update()
			if err != nil {
				t.Fatalf("update tcp memory metrics: %v", err)
			}

			var percent *metric.Data
			present := make(map[string]bool, len(metrics))
			for _, m := range metrics {
				present[m.Name()] = true
				if m.Name() == "usage_percent" {
					percent = m
				}
			}

			for _, want := range []string{"usage_pages", "usage_bytes", "limit_pages", "usage_percent"} {
				if !present[want] {
					t.Errorf("metric %q missing from the update result", want)
				}
			}

			if percent == nil {
				t.Fatal("usage_percent metric missing from the update result")
			}
			if percent.Value != tc.wantPercent {
				t.Errorf("usage_percent = %v, want %v", percent.Value, tc.wantPercent)
			}
		})
	}
}

func TestTcpMemoryUpdatePropagatesParseFailure(t *testing.T) {
	c := &tcpMemory{parseStats: func() (*tcpMemoryStat, error) {
		return nil, fs.ErrNotExist
	}}

	if _, err := c.Update(); err == nil {
		t.Fatal("update succeeded, want the parse error to propagate")
	}
}
