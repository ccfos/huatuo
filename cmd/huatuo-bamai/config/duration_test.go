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

package config

import (
	"fmt"
	"strconv"
	"testing"
)

func TestValidatePositiveDurationSeconds(t *testing.T) {
	for _, seconds := range []int{0, -1} {
		err := validatePositiveDurationSeconds("test interval", seconds)
		if err == nil || err.Error() != "test interval must be greater than zero seconds" {
			t.Errorf("seconds=%d: error = %v", seconds, err)
		}
	}
	if strconv.IntSize < 64 {
		return
	}

	limit := int64(maxDurationSeconds)
	if err := validatePositiveDurationSeconds("test interval", int(limit)); err != nil {
		t.Fatalf("largest representable interval rejected: %v", err)
	}
	err := validatePositiveDurationSeconds("test interval", int(limit+1))
	want := fmt.Sprintf("test interval must not exceed %d seconds", limit)
	if err == nil || err.Error() != want {
		t.Errorf("overflowing interval error = %v, want %q", err, want)
	}
}
