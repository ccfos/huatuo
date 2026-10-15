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

package main

import (
	"strings"
	"testing"
)

func TestCheckStatus(t *testing.T) {
	const active = "# TYPE huatuo_bamai_tracing_status_running gauge\n" +
		"huatuo_bamai_tracing_status_running{host=\"huatuo-dev\",region=\"e2e\"} 1\n" +
		"# TYPE huatuo_bamai_tracing_status_hitcount gauge\n" +
		"huatuo_bamai_tracing_status_hitcount{tracing=\"cpuidle\",host=\"huatuo-dev\",region=\"e2e\"} 0\n"
	const disabled = "# TYPE huatuo_bamai_tracing_status_running gauge\n" +
		"huatuo_bamai_tracing_status_running{host=\"huatuo-dev\",region=\"e2e\"} 0\n"
	for _, tc := range []struct {
		name, data string
		disabled   bool
		valid      bool
	}{
		{"active", active, false, true},
		{"disabled", disabled, true, true},
		{"missing", disabled, false, false},
		{"not disabled", active, true, false},
		{"restart", strings.Replace(active, "} 0", "} 1", 1), false, false},
		{"wrong region", strings.ReplaceAll(active, "e2e", "dev"), false, false},
		{"missing host", strings.ReplaceAll(active, "host=\"huatuo-dev\",", ""), false, false},
		{"not running", strings.Replace(active, "} 1", "} 0", 1), false, false},
		{"other tracer", strings.ReplaceAll(active, "cpuidle", "cpusys"), false, false},
		{"wrong metric type", strings.ReplaceAll(active, "gauge", "counter"), false, false},
		{"malformed", "invalid{", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkStatus(strings.NewReader(tc.data), tc.disabled); (err == nil) != tc.valid {
				t.Fatalf("checkStatus() error=%v; valid=%t", err, tc.valid)
			}
		})
	}
}
