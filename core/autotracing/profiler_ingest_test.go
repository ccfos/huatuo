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

package autotracing

import (
	"encoding/json"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

func TestParseProfilerEventTime(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantFixed  bool
		wantParsed time.Time
	}{
		{
			name:       "valid time",
			raw:        "2026-07-06 12:30:45.123 +0800",
			wantFixed:  true,
			wantParsed: time.Date(2026, 7, 6, 12, 30, 45, 123000000, time.FixedZone("", 8*3600)),
		},
		{
			name:      "empty falls back to now",
			raw:       "",
			wantFixed: false,
		},
		{
			name:      "malformed falls back to now",
			raw:       "not-a-time",
			wantFixed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := time.Now()
			got := parseProfilerEventTime(tt.raw)
			after := time.Now()

			if tt.wantFixed {
				if !got.Equal(tt.wantParsed) {
					t.Errorf("parseProfilerEventTime(%q) = %v, want %v", tt.raw, got, tt.wantParsed)
				}
				return
			}

			if got.Before(before) || got.After(after) {
				t.Errorf("parseProfilerEventTime(%q) = %v, want within [%v, %v]", tt.raw, got, before, after)
			}
		})
	}
}

func TestHandleProfilerEventNoStore(t *testing.T) {
	// With no profile store configured, SaveProfile is a no-op and the handler
	// must not error regardless of payload contents.
	ev := &ProfilerEvent{
		TracerID:      "tracer-123",
		ContainerID:   "container-abc",
		TracerName:    "profiler",
		TracerRunType: "autotracing",
		TracerTime:    "2026-07-06 12:30:45.123 +0800",
		TracerData:    &profctx.TracerData{},
	}

	if err := handleProfilerEvent(nil, ev); err != nil {
		t.Fatalf("handleProfilerEvent() error = %v, want nil", err)
	}
}

func TestProfilerEventPreservesPprofPayload(t *testing.T) {
	source := &ProfilerEvent{
		TracerID: "tracer-123",
		TracerData: &profctx.TracerData{
			FlameData: &profiler.ProfileData{
				ProfileType: profiler.ProfileTypeCpuSample,
				Profile: ptree.Profile{
					TimeNanos:     1700000000000000000,
					DurationNanos: int64(10 * time.Second),
				},
			},
		},
	}

	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	var decoded ProfilerEvent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if decoded.TracerData == nil || decoded.TracerData.FlameData == nil {
		t.Fatal("decoded profiler event is missing flamedata")
	}
	if got := decoded.TracerData.FlameData.Profile.TimeNanos; got != 1700000000000000000 {
		t.Errorf("decoded TimeNanos = %d, want 1700000000000000000", got)
	}
}
