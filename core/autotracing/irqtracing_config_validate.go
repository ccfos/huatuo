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

import (
	"fmt"
	"math"
)

const maxIRQTracingEventsPerSecond = uint64(math.MaxUint32) * 2

func validateIRQTracingConfig(config IRQTracingConfig) error {
	if err := validateTimerSeconds(config.Interval); err != nil {
		return fmt.Errorf("sampling interval: %w", err)
	}
	if err := validateTimerSeconds(config.IntervalTracing); err != nil {
		return fmt.Errorf("minimum trace interval: %w", err)
	}
	if err := validatePerfDurationSeconds(config.RunTracingToolTimeout); err != nil {
		return err
	}
	if config.MaxEventsPerSecond < 2 {
		return fmt.Errorf("max events per second must be at least 2, got %d", config.MaxEventsPerSecond)
	}
	if config.MaxEventsPerSecond > maxIRQTracingEventsPerSecond {
		return fmt.Errorf("max events per second must not exceed %d, got %d", maxIRQTracingEventsPerSecond, config.MaxEventsPerSecond)
	}
	if config.MinCPUs < 1 {
		return fmt.Errorf("minimum CPUs must be at least 1, got %d", config.MinCPUs)
	}
	if err := validateCPUPercentage(config.DeltaUsageThreshold); err != nil {
		return fmt.Errorf("usage delta threshold: %w", err)
	}
	if config.RelativeIncreaseThreshold < 0 {
		return fmt.Errorf("relative increase threshold must not be negative, got %d",
			config.RelativeIncreaseThreshold)
	}
	if config.SustainedIntervals < 1 {
		return fmt.Errorf("sustained intervals must be at least 1, got %d", config.SustainedIntervals)
	}
	if err := validateCPUPercentage(config.UsageThreshold); err != nil {
		return fmt.Errorf("usage threshold: %w", err)
	}
	return nil
}
