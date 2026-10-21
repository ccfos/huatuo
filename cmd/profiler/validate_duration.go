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

package main

import (
	"fmt"
	"time"
)

func validateAggregationWindow(duration, interval int) error {
	if duration < 1 {
		return fmt.Errorf("duration must be at least 1 second")
	}
	if interval < 1 {
		return fmt.Errorf("aggregation interval must be at least 1 second")
	}
	// Both values are converted to time.Duration in the sampling pipeline.
	// Reject seconds that would wrap the duration and stop profiling immediately.
	const maxSeconds = (1<<63 - 1) / int64(time.Second)
	if int64(duration) > maxSeconds {
		return fmt.Errorf("duration must not exceed %d seconds", maxSeconds)
	}
	if int64(interval) > maxSeconds {
		return fmt.Errorf("aggregation interval must not exceed %d seconds", maxSeconds)
	}
	if interval > duration {
		return fmt.Errorf(
			"aggregation interval (%ds) exceeds duration (%ds)",
			interval,
			duration,
		)
	}
	return nil
}
