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

type cpuTracingConfig struct {
	intervalSeconds         int64
	minTraceIntervalSeconds int64
	perfDurationSeconds     int64
	systemThreshold         int64
	systemDeltaThreshold    int64
}

func validateCPUConfig(config cpuTracingConfig) error {
	if err := validateTimerSeconds(config.intervalSeconds); err != nil {
		return fmt.Errorf("sampling interval: %w", err)
	}
	if err := validateTimerSeconds(config.minTraceIntervalSeconds); err != nil {
		return fmt.Errorf("minimum trace interval: %w", err)
	}
	if err := validatePerfDurationSeconds(config.perfDurationSeconds); err != nil {
		return err
	}
	if err := validateCPUPercentage(config.systemThreshold); err != nil {
		return fmt.Errorf("system threshold: %w", err)
	}
	if err := validateCPUPercentage(config.systemDeltaThreshold); err != nil {
		return fmt.Errorf("system delta threshold: %w", err)
	}

	return nil
}

func validateCPUIdleConfig(
	intervalSeconds int64,
	minTraceIntervalSeconds int64,
	perfDurationSeconds int64,
	threshold cpuIdleThreshold,
) error {
	if err := validateCPUConfig(cpuTracingConfig{
		intervalSeconds:         intervalSeconds,
		minTraceIntervalSeconds: minTraceIntervalSeconds,
		perfDurationSeconds:     perfDurationSeconds,
		systemThreshold:         threshold.percent.system,
		systemDeltaThreshold:    threshold.delta.system,
	}); err != nil {
		return err
	}
	if err := validateCPUPercentage(threshold.percent.user); err != nil {
		return fmt.Errorf("user threshold: %w", err)
	}
	if err := validateCPUPercentage(threshold.percent.total); err != nil {
		return fmt.Errorf("total threshold: %w", err)
	}
	if err := validateCPUPercentage(threshold.delta.user); err != nil {
		return fmt.Errorf("user delta threshold: %w", err)
	}
	if err := validateCPUPercentage(threshold.delta.total); err != nil {
		return fmt.Errorf("total delta threshold: %w", err)
	}

	return nil
}

func validateTimerSeconds(value int64) error {
	if value <= 0 {
		return fmt.Errorf("timer duration must be positive, got %d", value)
	}
	if value > maxTimerDurationSeconds {
		return fmt.Errorf(
			"timer duration must not exceed %d seconds, got %d",
			maxTimerDurationSeconds,
			value,
		)
	}

	return nil
}

func validatePerfDurationSeconds(value int64) error {
	if value <= 0 {
		return fmt.Errorf("perf duration must be positive, got %d", value)
	}
	if value > maxPerfDurationSeconds {
		return fmt.Errorf(
			"perf duration must not exceed %d seconds, got %d",
			maxPerfDurationSeconds,
			value,
		)
	}

	return nil
}

func validateCPUPercentage(value int64) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("cpu percentage must be between 0 and 100, got %d", value)
	}

	return nil
}
