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

package timeutil

import (
	"testing"
	"time"
)

func TestParseWithFallback(t *testing.T) {
	fallback := time.Date(2026, time.July, 21, 9, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	layout := "2006-01-02 15:04:05Z07:00"
	parsed := time.Date(2026, time.July, 21, 1, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		raw  string
		want time.Time
	}{
		{
			name: "valid timestamp is normalized to UTC",
			raw:  "2026-07-21 09:30:00+08:00",
			want: parsed,
		},
		{
			name: "empty timestamp uses UTC fallback",
			want: fallback.UTC(),
		},
		{
			name: "invalid timestamp uses UTC fallback",
			raw:  "not-a-time",
			want: fallback.UTC(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseWithFallback(tt.raw, layout, fallback); !got.Equal(tt.want) || got.Location() != time.UTC {
				t.Fatalf("ParseWithFallback(%q) = %v (%s), want %v (UTC)", tt.raw, got, got.Location(), tt.want)
			}
		})
	}
}
