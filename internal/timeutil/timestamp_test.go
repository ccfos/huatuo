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
