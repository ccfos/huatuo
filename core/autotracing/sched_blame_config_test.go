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
	"math"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validSchedBlameConfigForTest() SchedBlameConfig {
	return SchedBlameConfig{
		TargetContainerScope:       schedBlameTargetContainerScopeNormal,
		SliceDropPercent:           0,
		ExternalAnomalyK:           1,
		SliceBatchSize:             128,
		PerfEventPerCPUBufferBytes: 512 * 1024,
		PerfEventWatermarkBytes:    8 * 1024,
		PerfEventQueueRecords:      8192,
		PollIntervalMs:             100,
	}
}

func TestSchedBlameConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*SchedBlameConfig)
		wantErr string
	}{
		{
			name: "scope",
			mutate: func(config *SchedBlameConfig) {
				config.TargetContainerScope = "unknown"
			},
			wantErr: "unsupported target container scope",
		},
		{
			name: "drop percent",
			mutate: func(config *SchedBlameConfig) {
				config.SliceDropPercent = 100
			},
			wantErr: "slice drop percent",
		},
		{
			name: "anomaly multiplier",
			mutate: func(config *SchedBlameConfig) {
				config.ExternalAnomalyK = math.NaN()
			},
			wantErr: "external anomaly multiplier",
		},
		{
			name: "batch size",
			mutate: func(config *SchedBlameConfig) {
				config.SliceBatchSize = 129
			},
			wantErr: "slice batch size",
		},
		{
			name: "watermark after capacity normalization",
			mutate: func(config *SchedBlameConfig) {
				config.PerfEventPerCPUBufferBytes = 4097
				config.PerfEventWatermarkBytes = 8192
			},
			wantErr: "must be smaller than effective per-CPU buffer bytes 8192",
		},
		{
			name: "queue capacity",
			mutate: func(config *SchedBlameConfig) {
				config.PerfEventQueueRecords = 0
			},
			wantErr: "perf event queue records",
		},
		{
			name: "poll interval",
			mutate: func(config *SchedBlameConfig) {
				config.PollIntervalMs = 1001
			},
			wantErr: "poll interval",
		},
	}

	config := validSchedBlameConfigForTest()
	require.NoError(t, config.Validate())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := validSchedBlameConfigForTest()
			test.mutate(&invalid)
			assert.ErrorContains(t, invalid.Validate(), test.wantErr)
		})
	}
}

func TestSchedBlameRuntimeConfigIsImmutable(t *testing.T) {
	oldConfig := configSnapshot()
	t.Cleanup(func() { Set(oldConfig) })
	config := &Config{}
	config.SchedBlame = SchedBlameConfig{
		TargetContainerScope:       schedBlameTargetContainerScopeAll,
		TargetQos:                  []string{" Burstable ", "guaranteed"},
		HighlightContainer:         " target ",
		SliceDropPercent:           25,
		ExternalAnomalyK:           2,
		SliceBatchSize:             64,
		PerfEventPerCPUBufferBytes: 256 * 1024,
		PerfEventWatermarkBytes:    4 * 1024,
		PerfEventQueueRecords:      4096,
		PollIntervalMs:             50,
		ExternalRatioDebugFile:     " ratios.csv ",
	}
	Set(config)
	runtimeConfig := schedBlameRuntimeConfigSnapshot()

	Set(&Config{})
	assert.Equal(t, schedBlameTargetContainerScopeAll,
		runtimeConfig.targetContainerScope)
	assert.Equal(t, []string{"burstable", "guaranteed"}, runtimeConfig.targetQos)
	assert.Equal(t, "target", runtimeConfig.highlightContainer)
	assert.Equal(t, uint32(25), runtimeConfig.sliceDropPercent)
	assert.Equal(t, 2.0, runtimeConfig.externalAnomalyK)
	assert.Equal(t, uint32(64), runtimeConfig.sliceBatchSize)
	assert.Equal(t, uint32(256*1024), runtimeConfig.perfEventPerCPUBufferBytes)
	assert.Equal(t, uint32(4*1024), runtimeConfig.perfEventWatermarkBytes)
	assert.Equal(t, 4096, runtimeConfig.perfEventQueueRecords)
	assert.Equal(t, 50*time.Millisecond, runtimeConfig.pollInterval)
	assert.Equal(t, "ratios.csv", runtimeConfig.externalRatioDebugFile)
}

func TestSchedBlameEffectivePerfBufferBytes(t *testing.T) {
	pageSize := uint32(os.Getpagesize())
	assert.Equal(t, uint64(pageSize),
		schedBlameEffectivePerfBufferBytes(1, pageSize))
	assert.Equal(t, uint64(pageSize),
		schedBlameEffectivePerfBufferBytes(pageSize, pageSize))
	assert.Equal(t, uint64(pageSize)*2,
		schedBlameEffectivePerfBufferBytes(pageSize+1, pageSize))
}
