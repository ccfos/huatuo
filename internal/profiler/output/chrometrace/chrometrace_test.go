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

package chrometrace

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

func TestCounterEventNonfiniteValuesRemainStrings(t *testing.T) {
	formatter := New(100)
	if err := formatter.Add(&output.Sample{
		Tags: map[string]string{
			"nan": "NaN", "positive": "+Inf", "negative": "-Inf",
			"finite": "1.25",
		},
	}); err != nil {
		t.Fatal(err)
	}

	var encoded bytes.Buffer
	if err := formatter.Write(&encoded); err != nil {
		t.Fatalf("write Chrome trace: %v", err)
	}

	var trace struct {
		TraceEvents []struct {
			Args map[string]any `json:"args"`
		} `json:"traceEvents"`
	}
	if err := json.Unmarshal(encoded.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	args := trace.TraceEvents[0].Args
	for key, value := range map[string]any{
		"nan": "NaN", "positive": "+Inf", "negative": "-Inf", "finite": 1.25,
	} {
		if args[key] != value {
			t.Errorf("%s = %v, want %v", key, args[key], value)
		}
	}
}
