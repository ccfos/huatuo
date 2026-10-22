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
	"encoding/json"
	"testing"
	"time"
)

func TestTimestampJSON(t *testing.T) {
	for _, input := range []string{
		`"2026-09-17T08:00:00+08:00"`,
		`"2026-09-17T00:00:00.123Z"`,
		`"2026-09-17T00:00:00.123456789Z"`,
		`"0001-01-01T00:00:00Z"`,
	} {
		t.Run(input, func(t *testing.T) {
			var timestamp Timestamp
			if err := json.Unmarshal([]byte(input), &timestamp); err != nil {
				t.Fatal(err)
			}
			var expected time.Time
			if err := json.Unmarshal([]byte(input), &expected); err != nil {
				t.Fatal(err)
			}
			if !timestamp.Equal(expected) {
				t.Fatalf("decoded time = %v, want %v", timestamp, expected)
			}
			// Values, pointers, and map entries must share the same encoding.
			for _, value := range []any{timestamp, &timestamp} {
				encoded, err := json.Marshal(map[string]any{"timestamp": value})
				if err != nil {
					t.Fatal(err)
				}
				want := `{"timestamp":"` + FormatUTC(expected) + `"}`
				if string(encoded) != want {
					t.Fatalf("JSON = %s, want %s", encoded, want)
				}
			}
		})
	}
}

func TestTimestampJSONInvalid(t *testing.T) {
	for _, input := range []string{`"invalid"`, `"2026-02-30T00:00:00Z"`, `123`, `{}`, `""`} {
		t.Run(input, func(t *testing.T) {
			var timestamp Timestamp
			if err := json.Unmarshal([]byte(input), &timestamp); err == nil {
				t.Fatalf("accepted invalid timestamp %s", input)
			}
		})
	}
}

func TestTimestampJSONNull(t *testing.T) {
	var value struct {
		Timestamp *Timestamp `json:"timestamp,omitempty"`
	}
	if err := json.Unmarshal([]byte(`{"timestamp":null}`), &value); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if value.Timestamp != nil || string(encoded) != `{}` {
		t.Fatalf("absent timestamp = %s", encoded)
	}
}

func TestTimestampJSONRejectsUnrepresentableUTCYear(t *testing.T) {
	for _, input := range []time.Time{
		time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(0, 1, 1, 0, 0, 0, 0, time.FixedZone("east", 3600)),
		time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("west", -7200)),
	} {
		timestamp := Timestamp{Time: input}
		if encoded, err := json.Marshal(timestamp); err == nil {
			t.Fatalf("encoded unrepresentable UTC year %d as %s", input.UTC().Year(), encoded)
		}
	}
}

func TestTimestampJSONBoundaryYearsRoundTrip(t *testing.T) {
	for _, input := range []time.Time{
		time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.FixedZone("east", 3600)),
		time.Date(-1, 12, 31, 23, 0, 0, 0, time.FixedZone("west", -7200)),
	} {
		encoded, err := json.Marshal(Timestamp{Time: input})
		if err != nil {
			t.Fatal(err)
		}
		var decoded Timestamp
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("cannot read serialized timestamp %s: %v", encoded, err)
		}
		if !decoded.Equal(input) {
			t.Fatalf("roundtrip=%v, want %v", decoded.Time, input)
		}
	}
}

func TestTimestampJSONTimezoneInputOutsideUTCYearRange(t *testing.T) {
	var timestamp Timestamp
	if err := json.Unmarshal([]byte(`"9999-12-31T23:00:00-02:00"`), &timestamp); err != nil {
		t.Fatal(err)
	}
	if encoded, err := json.Marshal(timestamp); err == nil {
		t.Fatalf("accepted UTC year 10000: %s", encoded)
	}
}

func BenchmarkTimestampMarshalJSON(b *testing.B) {
	timestamp := Timestamp{Time: time.Date(2026, 10, 19, 12, 34, 56, 123456789, time.UTC)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := timestamp.MarshalJSON(); err != nil {
			b.Fatal(err)
		}
	}
}
