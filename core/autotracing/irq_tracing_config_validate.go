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

func validateIrqTracingConfig(config IRQTracingConfig) error {
	if err := validateTimerSeconds(config.Interval); err != nil {
		return fmt.Errorf("sampling interval: %w", err)
	}
	if err := validateTimerSeconds(config.IntervalTracing); err != nil {
		return fmt.Errorf("minimum trace interval: %w", err)
	}
	if err := validatePerfDurationSeconds(config.RunTracingToolTimeout); err != nil {
		return err
	}
	if config.SpikeMinCPUs < 1 {
		return fmt.Errorf("spike min cpus must be at least 1, got %d", config.SpikeMinCPUs)
	}
	if err := validateCPUPercentage(config.SpikeAbsDeltaThreshold); err != nil {
		return fmt.Errorf("spike absolute delta threshold: %w", err)
	}
	if config.SpikeRelIncreasePct < 0 {
		return fmt.Errorf("spike relative increase must not be negative, got %d", config.SpikeRelIncreasePct)
	}
	if config.SustainedConsecutiveIntervals < 1 {
		return fmt.Errorf("sustained consecutive intervals must be at least 1, got %d", config.SustainedConsecutiveIntervals)
	}
	if err := validateCPUPercentage(config.SustainedUtilThreshold); err != nil {
		return fmt.Errorf("sustained util threshold: %w", err)
	}
	return nil
}
