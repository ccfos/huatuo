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
	"encoding/json"
	"testing"

	"github.com/ccfos/huatuo/internal/flamegraph"
)

func TestCheckFrames(t *testing.T) {
	valid := []flamegraph.FrameData{
		{Level: 0, Value: 10, Label: "total"},
		{Level: 1, Value: 10, Self: 2, Label: "cpuidle-a"},
		{Level: 2, Value: 5, Self: 5, Label: "user_work"},
		{Level: 2, Value: 3, Self: 3, Label: "generic_kernel_work_[k]"},
	}
	for _, tc := range []struct {
		name   string
		change func([]flamegraph.FrameData)
		kernel bool
		valid  bool
	}{
		{"user", nil, false, true},
		{"system", nil, true, true},
		{"missing worker", func(f []flamegraph.FrameData) { f[1].Label = "other" }, false, false},
		{"unrelated worker", func(f []flamegraph.FrameData) { f[2].Label = "cpuidle-b" }, false, false},
		{"missing kernel", func(f []flamegraph.FrameData) { f[3].Label = "user_work" }, true, false},
		{"bad nesting", func(f []flamegraph.FrameData) { f[2].Level = 3 }, false, false},
		{"wrong totals", func(f []flamegraph.FrameData) { f[0].Value++ }, false, false},
		{"negative self", func(f []flamegraph.FrameData) { f[1].Self = -1 }, false, false},
		{"multiple roots", func(f []flamegraph.FrameData) { f[3].Level = 0 }, false, false},
		{"empty label", func(f []flamegraph.FrameData) { f[3].Label = "" }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := append([]flamegraph.FrameData(nil), valid...)
			if tc.change != nil {
				tc.change(frames)
			}
			if err := checkFrames(frames, "cpuidle-a", "cpuidle-b", tc.kernel); (err == nil) != tc.valid {
				t.Fatalf("checkFrames() error=%v; valid=%t", err, tc.valid)
			}
		})
	}
	for _, frames := range [][]flamegraph.FrameData{nil, {{Value: 1, Label: "total"}}} {
		if err := checkFrames(frames, "cpuidle-a", "", false); err == nil {
			t.Fatal("empty flame graph passed")
		}
	}
	data, err := json.Marshal([]map[string]any{{"tracer_data": map[string]any{"flamedata": valid}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkFlames(data, "cpuidle-a", "cpuidle-b", true); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"[]", "{}", `[{"tracer_data":{}}]`} {
		if err := checkFlames([]byte(data), "cpuidle-a", "", false); err == nil {
			t.Fatalf("invalid event %s passed", data)
		}
	}
}
