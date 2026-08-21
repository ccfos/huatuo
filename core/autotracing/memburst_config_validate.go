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

package autotracing

import "fmt"

func validateMemBurst(c *MemBurstConfig) error {
	if c.DeltaMemoryBurst <= 0 {
		return fmt.Errorf("memory burst delta must be positive, got %d", c.DeltaMemoryBurst)
	}
	if c.DeltaAnonThreshold < 0 || c.DeltaAnonThreshold > 100 {
		return fmt.Errorf("memory burst anon threshold must be in [0, 100], got %d", c.DeltaAnonThreshold)
	}
	if c.Interval <= 0 {
		return fmt.Errorf("memory burst interval must be positive, got %d", c.Interval)
	}
	if c.IntervalTracing <= 0 {
		return fmt.Errorf("memory burst tracing interval must be positive, got %d", c.IntervalTracing)
	}
	if c.SlidingWindowLength <= 0 {
		return fmt.Errorf("memory burst sliding window length must be positive, got %d", c.SlidingWindowLength)
	}
	if c.DumpProcessMaxNum <= 0 {
		return fmt.Errorf("memory burst dump process max num must be positive, got %d", c.DumpProcessMaxNum)
	}
	return nil
}
