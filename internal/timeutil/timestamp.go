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
	"fmt"
	"time"
)

// Timestamp stores a time value and emits JSON in UTC with nine fractional digits.
// The embedded time.Time accepts RFC3339 JSON through its UnmarshalJSON method.
// JSON encoding rejects years outside 0000..9999 after conversion to UTC.
// Use Time when passing the value to APIs that require time.Time.
type Timestamp struct {
	time.Time
}

// Now returns the current time with Timestamp's JSON formatting.
func Now() Timestamp {
	return Timestamp{Time: time.Now()}
}

// FormatUTC returns UTC text with fixed nanosecond precision.
func (t Timestamp) FormatUTC() string {
	return FormatUTC(t.Time)
}

func (t Timestamp) MarshalJSON() ([]byte, error) {
	utc := t.UTC()
	// UTC conversion can cross the RFC3339 year boundary even for valid input.
	if year := utc.Year(); year < 0 || year > 9999 {
		return nil, fmt.Errorf("serialize timestamp: UTC year %d is outside RFC3339 range 0000..9999", year)
	}

	// FormatUTC contains only date digits and separators, so no JSON escaping is needed.
	encoded := make([]byte, 1, 32)
	encoded[0] = '"'
	encoded = utc.AppendFormat(encoded, Layout)
	return append(encoded, '"'), nil
}
