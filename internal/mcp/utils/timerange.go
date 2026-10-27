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
	"fmt"
	"strings"
	"time"
)

const (
	TimeRangeNow = "now"
)

// ParseTimeRange accepts a duration, an RFC3339 start time, or a start/end
// range. A zero maximum disables the upper bound.
func ParseTimeRange(value string, maximum time.Duration) (start, end time.Time, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return start, end, fmt.Errorf("time_range is required")
	}
	end = time.Now().UTC()
	if duration, parseErr := time.ParseDuration(value); parseErr == nil {
		if err := validateTimeRangeDuration(duration, maximum); err != nil {
			return start, end, err
		}
		return end.Add(-duration), end, nil
	}
	if strings.Contains(value, "/") {
		parts := strings.SplitN(value, "/", 2)
		start, err = time.Parse(time.RFC3339, strings.TrimSpace(parts[0]))
		if err != nil {
			return start, end, fmt.Errorf("invalid start time: %w", err)
		}
		if strings.TrimSpace(parts[1]) == TimeRangeNow {
			end = time.Now().UTC()
		} else {
			end, err = time.Parse(time.RFC3339, strings.TrimSpace(parts[1]))
			if err != nil {
				return start, end, fmt.Errorf("invalid end time: %w", err)
			}
		}
		return validateTimeRangeBounds(start, end, maximum)
	}
	start, err = time.Parse(time.RFC3339, value)
	if err != nil {
		return start, end, fmt.Errorf(
			"invalid time range format: %w (expected duration, RFC3339, or RFC3339/RFC3339)",
			err,
		)
	}
	return validateTimeRangeBounds(start, end, maximum)
}

func validateTimeRangeBounds(start, end time.Time, maximum time.Duration) (time.Time, time.Time, error) {
	if err := validateTimeRangeDuration(end.Sub(start), maximum); err != nil {
		return start, end, err
	}
	return start, end, nil
}

func validateTimeRangeDuration(duration, maximum time.Duration) error {
	if duration <= 0 {
		return fmt.Errorf("range must be positive")
	}
	if maximum > 0 && duration > maximum {
		return fmt.Errorf("range must not exceed %s", maximum)
	}
	return nil
}
