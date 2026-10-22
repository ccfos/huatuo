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

package utils

import (
	"testing"
	"time"
)

func TestParseTimeRangeRequiredAndConfigurableMaximum(t *testing.T) {
	if _, _, err := ParseTimeRange("", 72*time.Hour); err == nil {
		t.Fatal("Parse() accepted an empty range")
	}
	if _, _, err := ParseTimeRange("7d", 0); err == nil {
		t.Fatal("Parse() accepted a non-Go duration")
	}
	if _, _, err := ParseTimeRange("168h", 72*time.Hour); err == nil {
		t.Fatal("Parse() accepted a range above the configured maximum")
	}
	if _, _, err := ParseTimeRange("336h", 0); err != nil {
		t.Fatalf("Parse() unlimited range error = %v", err)
	}
}

func TestParseTimeRangeExplicitRange(t *testing.T) {
	start, end, err := ParseTimeRange("2026-09-01T00:00:00Z/2026-09-15T00:00:00Z", 0)
	if err != nil {
		t.Fatal(err)
	}
	if end.Sub(start) != 14*24*time.Hour {
		t.Fatalf("range = %s", end.Sub(start))
	}
}
