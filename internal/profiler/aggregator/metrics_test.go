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

package aggregator

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewMetricsCapturesOverflowCountAndStartTime(t *testing.T) {
	before := time.Now()
	got := newMetrics(7)
	after := time.Now()

	if got.AggrOverflowCount != 7 {
		t.Errorf("AggrOverflowCount = %d, want 7", got.AggrOverflowCount)
	}
	if got.StartTime.Before(before) || got.StartTime.After(after) {
		t.Errorf("StartTime = %v, want between %v and %v", got.StartTime, before, after)
	}
}

func TestMetricsJSONFieldNames(t *testing.T) {
	data, err := json.Marshal(&metrics{AggrOverflowCount: 3})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := decoded["start_time"]; !ok {
		t.Errorf("JSON fields = %v, missing start_time", decoded)
	}
	if got := decoded["aggr_overflow_count"]; got != float64(3) {
		t.Errorf("aggr_overflow_count = %v, want 3", got)
	}
}
