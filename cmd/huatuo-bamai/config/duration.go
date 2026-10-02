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
	"time"
)

const maxDurationSeconds = int64((1<<63 - 1) / int64(time.Second))

func validatePositiveDurationSeconds(name string, seconds int) error {
	if seconds <= 0 {
		return fmt.Errorf("%s must be greater than zero seconds", name)
	}
	if int64(seconds) > maxDurationSeconds {
		return fmt.Errorf("%s must not exceed %d seconds", name, maxDurationSeconds)
	}
	return nil
}
