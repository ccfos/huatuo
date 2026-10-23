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
	"testing"

	"huatuo-bamai/pkg/tracing"
)

func TestNewMemBurstBindsConfig(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() {
		Set(originalConfig)
	})

	testConfig := &Config{}
	testConfig.MemoryBurst.DeltaMemoryBurst = 100
	testConfig.MemoryBurst.DeltaAnonThreshold = 70
	testConfig.MemoryBurst.Interval = 7
	testConfig.MemoryBurst.IntervalTracing = 30
	testConfig.MemoryBurst.SlidingWindowLength = 60
	testConfig.MemoryBurst.DumpProcessMaxNum = 10
	Set(testConfig)

	attr, err := newMemBurst()
	if err != nil {
		t.Fatalf("newMemBurst() error = %v", err)
	}
	if attr.Interval != 7 {
		t.Errorf("Interval = %d, want 7 (must follow MemoryBurst.Interval)", attr.Interval)
	}
	if attr.Flag != tracing.FlagTracing {
		t.Errorf("Flag = %d, want tracing.FlagTracing", attr.Flag)
	}
}

func TestNewMemBurstRejectsInvalidConfig(t *testing.T) {
	originalConfig := configSnapshot()
	t.Cleanup(func() {
		Set(originalConfig)
	})

	validConfig := &Config{}
	validConfig.MemoryBurst.DeltaMemoryBurst = 100
	validConfig.MemoryBurst.DeltaAnonThreshold = 70
	validConfig.MemoryBurst.Interval = 10
	validConfig.MemoryBurst.IntervalTracing = 1800
	validConfig.MemoryBurst.SlidingWindowLength = 60
	validConfig.MemoryBurst.DumpProcessMaxNum = 10

	tests := []struct {
		name        string
		update      func(*Config)
		expectedErr string
	}{
		{
			name: "valid config",
		},
		{
			name: "non-positive sampling interval",
			update: func(config *Config) {
				config.MemoryBurst.Interval = 0
			},
			expectedErr: "validate memory burst config: memory burst interval must be positive, got 0",
		},
		{
			name: "non-positive tracing interval",
			update: func(config *Config) {
				config.MemoryBurst.IntervalTracing = -1
			},
			expectedErr: "validate memory burst config: memory burst tracing interval must be positive, got -1",
		},
		{
			name: "non-positive sliding window length",
			update: func(config *Config) {
				config.MemoryBurst.SlidingWindowLength = 0
			},
			expectedErr: "validate memory burst config: memory burst sliding window length must be positive, got 0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig.Clone()
			if test.update != nil {
				test.update(config)
			}
			Set(config)

			_, err := newMemBurst()
			if test.expectedErr == "" {
				if err != nil {
					t.Fatalf("newMemBurst() error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != test.expectedErr {
				t.Fatalf("newMemBurst() error = %v, want %q", err, test.expectedErr)
			}
		})
	}
}
