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

func TestTaskState(t *testing.T) {
	for _, state := range []string{"D", "S", "R", "I"} {
		got, err := taskState("Name:\treader\nState:\t" + state + " (state)\n")
		if err != nil || got != state {
			t.Fatalf("taskState() = %q, %v; want %q", got, err, state)
		}
	}
	for _, data := range []string{"", "State:\n", "State: sleeping"} {
		if _, err := taskState(data); err == nil {
			t.Fatalf("taskState(%q) accepted an invalid status", data)
		}
	}
}

func TestCheckStatus(t *testing.T) {
	const running = "# TYPE huatuo_bamai_tracing_status_running gauge\n"
	const hit = "# TYPE huatuo_bamai_tracing_status_hitcount gauge\n"
	const active = running + "huatuo_bamai_tracing_status_running{host=\"huatuo-dev\",region=\"e2e\"} 1\n" +
		hit + "huatuo_bamai_tracing_status_hitcount{tracing=\"dload\",host=\"huatuo-dev\",region=\"e2e\"} 0\n"
	const inactive = running + "huatuo_bamai_tracing_status_running{host=\"huatuo-dev\",region=\"e2e\"} 0\n"
	for _, tc := range []struct {
		name     string
		data     string
		disabled bool
		valid    bool
	}{
		{"active", active, false, true},
		{"disabled", inactive, true, true},
		{"disabled still active", active, true, false},
		{"missing tracer", inactive, false, false},
		{"restart", strings.Replace(active, "} 0", "} 1", 1), false, false},
		{"not running", strings.Replace(active, "} 1", "} 0", 1), false, false},
		{"other tracer", strings.ReplaceAll(active, "dload", "runqlat"), false, false},
		{"missing host", strings.ReplaceAll(active, "host=\"huatuo-dev\",", ""), false, false},
		{"wrong region", strings.ReplaceAll(active, "region=\"e2e\"", "region=\"dev\""), false, false},
		{"malformed", "invalid{", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkStatus(strings.NewReader(tc.data), tc.disabled); (err == nil) != tc.valid {
				t.Fatalf("checkStatus() error=%v; valid=%t", err, tc.valid)
			}
		})
	}
}
