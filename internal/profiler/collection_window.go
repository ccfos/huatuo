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

package profiler

import (
	"errors"
	"time"
)

// CollectionWindow identifies aggregation boundaries, not per-event timestamps.
type CollectionWindow struct {
	Start time.Time
	End   time.Time
}

// Validate permits an instantaneous snapshot but rejects unknown boundaries.
func (w CollectionWindow) Validate() error {
	if w.Start.IsZero() {
		return errors.New("collection window start is required")
	}
	if w.End.IsZero() {
		return errors.New("collection window end is required")
	}
	if w.End.Before(w.Start) {
		return errors.New("collection window end precedes start")
	}
	return nil
}

// Duration returns the elapsed time between validated boundaries.
func (w CollectionWindow) Duration() time.Duration {
	return w.End.Sub(w.Start)
}
