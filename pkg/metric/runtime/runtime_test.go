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

package runtime

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRegisterCollector(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		expected  []string
	}{
		{
			name:      "register with huatuo namespace",
			namespace: "huatuo",
			expected:  []string{"huatuo_go_goroutines", "huatuo_process_start_time_seconds"},
		},
		{
			name:      "register with null namespace",
			namespace: "",
			expected:  []string{"_go_goroutines", "process_start_time_seconds"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			RegisterCollector(reg, tt.namespace)
			families, err := reg.Gather()
			if err != nil {
				t.Errorf("Gather() returned error: %v", err)
				return
			}
			metricSet := make(map[string]struct{}, len(families))
			for _, f := range families {
				metricSet[f.GetName()] = struct{}{}
			}
			for _, exp := range tt.expected {
				if _, ok := metricSet[exp]; !ok {
					t.Errorf("expected metric %q to be registered", exp)
				}
			}
		})
	}
}
