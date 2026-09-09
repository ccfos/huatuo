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
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ccfos/huatuo/internal/matcher"
)

const (
	schedBlameTargetContainerScopeAll    = "all"
	schedBlameTargetContainerScopeNormal = "normal"
	defaultSchedBlameSliceDropPercent    = uint32(0)
	defaultSchedBlameExternalAnomalyK    = 1.0
)

// ContainerFilterConfig is the serializable form of a container filter.
// It is converted to a *matcher.ContainerMatcher at runtime.
type ContainerFilterConfig struct {
	Included []*matcher.Rule `toml:"Included,omitempty"`
	Excluded []*matcher.Rule `toml:"Excluded,omitempty"`
}

// Build compiles the config into a ContainerMatcher.
// Returns nil, nil when the config is nil (no filtering).
func (c *ContainerFilterConfig) Build() (*matcher.ContainerMatcher, error) {
	if c == nil {
		return nil, nil
	}
	return matcher.NewContainerMatcherFromRules(c.Included, c.Excluded)
}

// MemBurstConfig holds memory burst autotracing configuration.
type MemBurstConfig struct {
	DeltaMemoryBurst    int `default:"100"`
	DeltaAnonThreshold  int `default:"70"`
	Interval            int `default:"10"`
	IntervalTracing     int `default:"1800"`
	SlidingWindowLength int `default:"60"`
	DumpProcessMaxNum   int `default:"10"`
}

// SchedBlameConfig controls scheduler contention attribution.
type SchedBlameConfig struct {
	TargetContainerScope       string `default:"normal"`
	TargetQos                  []string
	HighlightContainer         string
	SliceDropPercent           uint32
	ExternalAnomalyK           float64 `default:"1.0"`
	SliceBatchSize             uint32  `default:"128"`
	PerfEventPerCPUBufferBytes uint32  `default:"524288"`
	PerfEventWatermarkBytes    uint32  `default:"8192"`
	PerfEventQueueRecords      int     `default:"8192"`
	PollIntervalMs             uint32  `default:"100"`
	ExternalRatioDebugFile     string
}

type schedBlameRuntimeConfig struct {
	targetContainerScope       string
	targetQos                  []string
	highlightContainer         string
	sliceDropPercent           uint32
	externalAnomalyK           float64
	sliceBatchSize             uint32
	perfEventPerCPUBufferBytes uint32
	perfEventWatermarkBytes    uint32
	perfEventQueueRecords      int
	pollInterval               time.Duration
	externalRatioDebugFile     string
}

// Validate rejects invalid SchedBlame settings before BPF is loaded.
func (c *SchedBlameConfig) Validate() error {
	switch strings.ToLower(strings.TrimSpace(c.TargetContainerScope)) {
	case "normal", "all":
	default:
		return fmt.Errorf("unsupported target container scope %q", c.TargetContainerScope)
	}
	if c.SliceDropPercent > 99 {
		return fmt.Errorf("slice drop percent must be in 0..99, got %d", c.SliceDropPercent)
	}
	if math.IsNaN(c.ExternalAnomalyK) || math.IsInf(c.ExternalAnomalyK, 0) ||
		c.ExternalAnomalyK < 0 {
		return fmt.Errorf("external anomaly multiplier must be finite and nonnegative, got %v", c.ExternalAnomalyK)
	}
	if c.SliceBatchSize < 1 || c.SliceBatchSize > 128 {
		return fmt.Errorf("slice batch size must be in 1..128, got %d", c.SliceBatchSize)
	}
	if c.PerfEventPerCPUBufferBytes == 0 {
		return errors.New("perf event per-CPU buffer bytes must be positive")
	}
	if c.PerfEventWatermarkBytes == 0 {
		return errors.New("perf event watermark bytes must be positive")
	}
	effectiveBufferBytes := schedBlameEffectivePerfBufferBytes(
		c.PerfEventPerCPUBufferBytes,
		uint32(os.Getpagesize()),
	)
	if uint64(c.PerfEventWatermarkBytes) >= effectiveBufferBytes {
		return fmt.Errorf(
			"perf event watermark bytes %d must be smaller than effective per-CPU buffer bytes %d",
			c.PerfEventWatermarkBytes,
			effectiveBufferBytes,
		)
	}
	if c.PerfEventQueueRecords <= 0 {
		return errors.New("perf event queue records must be positive")
	}
	if c.PollIntervalMs < 1 || c.PollIntervalMs > 1000 {
		return fmt.Errorf("poll interval must be in 1..1000 ms, got %d", c.PollIntervalMs)
	}
	return nil
}

func schedBlameEffectivePerfBufferBytes(requested, pageSize uint32) uint64 {
	pages := (uint64(requested) + uint64(pageSize) - 1) / uint64(pageSize)
	capacityPages := uint64(1)
	for capacityPages < pages {
		capacityPages <<= 1
	}
	return capacityPages * uint64(pageSize)
}

// Config holds autotracing configuration.
type Config struct {
	CPUIdle struct {
		UserThreshold         int64                  `default:"75"`
		SysThreshold          int64                  `default:"45"`
		UsageThreshold        int64                  `default:"90"`
		DeltaUserThreshold    int64                  `default:"45"`
		DeltaSysThreshold     int64                  `default:"20"`
		DeltaUsageThreshold   int64                  `default:"55"`
		Interval              int64                  `default:"10"`
		IntervalTracing       int64                  `default:"1800"`
		RunTracingToolTimeout int64                  `default:"10"`
		Filter                *ContainerFilterConfig `toml:"Filter"`
	}

	CPUSys struct {
		SysThreshold          int64 `default:"45"`
		DeltaSysThreshold     int64 `default:"20"`
		Interval              int64 `default:"10"`
		IntervalTracing       int64 `default:"1800"`
		RunTracingToolTimeout int64 `default:"10"`
	}

	Dload struct {
		ThresholdLoad   int64 `default:"5"`
		Interval        int64 `default:"10"`
		IntervalTracing int64 `default:"1800"`
		EnableDebug     bool  `default:"false"`
	}

	IOTracing struct {
		RbpsThreshold         uint64 `default:"2000"`
		WbpsThreshold         uint64 `default:"1500"`
		UtilThreshold         uint64 `default:"90"`
		AwaitThreshold        uint64 `default:"100"`
		RunTracingToolTimeout uint64 `default:"10"`
		MaxProcDump           int    `default:"10"`
		MaxFilesPerProcDump   int    `default:"5"`
	}

	MemoryBurst MemBurstConfig

	SchedBlame SchedBlameConfig

	// IssuesList for known issue filtering
	IssuesList [][]string
}

var currentConfig atomic.Pointer[Config]

func init() {
	currentConfig.Store(&Config{})
}

// Set atomically publishes an immutable copy of the autotracing config. A nil
// argument resets it to the zero value.
func Set(c *Config) {
	currentConfig.Store(c.Clone())
}

func configSnapshot() *Config {
	return currentConfig.Load()
}

func schedBlameConfigSnapshot() SchedBlameConfig {
	return configSnapshot().SchedBlame
}

func schedBlameRuntimeConfigSnapshot() schedBlameRuntimeConfig {
	config := schedBlameConfigSnapshot()
	targetContainerScope := strings.ToLower(strings.TrimSpace(
		config.TargetContainerScope,
	))
	if targetContainerScope == "" {
		targetContainerScope = schedBlameTargetContainerScopeNormal
	}
	targetQos := make([]string, 0, len(config.TargetQos))
	for _, value := range config.TargetQos {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			targetQos = append(targetQos, value)
		}
	}
	slices.Sort(targetQos)
	targetQos = slices.Compact(targetQos)
	sliceBatchSize := config.SliceBatchSize
	if sliceBatchSize == 0 {
		sliceBatchSize = 128
	}
	perfEventPerCPUBufferBytes := config.PerfEventPerCPUBufferBytes
	if perfEventPerCPUBufferBytes == 0 {
		perfEventPerCPUBufferBytes = 512 * 1024
	}
	perfEventWatermarkBytes := config.PerfEventWatermarkBytes
	if perfEventWatermarkBytes == 0 {
		perfEventWatermarkBytes = 8 * 1024
	}
	perfEventQueueRecords := config.PerfEventQueueRecords
	if perfEventQueueRecords <= 0 {
		perfEventQueueRecords = 8192
	}
	pollIntervalMs := config.PollIntervalMs
	if pollIntervalMs == 0 {
		pollIntervalMs = 100
	}
	return schedBlameRuntimeConfig{
		targetContainerScope:       targetContainerScope,
		targetQos:                  targetQos,
		highlightContainer:         strings.TrimSpace(config.HighlightContainer),
		sliceDropPercent:           config.SliceDropPercent,
		externalAnomalyK:           config.ExternalAnomalyK,
		sliceBatchSize:             sliceBatchSize,
		perfEventPerCPUBufferBytes: perfEventPerCPUBufferBytes,
		perfEventWatermarkBytes:    perfEventWatermarkBytes,
		perfEventQueueRecords:      perfEventQueueRecords,
		pollInterval:               time.Duration(pollIntervalMs) * time.Millisecond,
		externalRatioDebugFile:     strings.TrimSpace(config.ExternalRatioDebugFile),
	}
}

func (config *schedBlameRuntimeConfig) highlightConfigured() bool {
	return config.highlightContainer != ""
}

// Clone returns a deep copy suitable for immutable publication.
func (c *Config) Clone() *Config {
	if c == nil {
		return &Config{}
	}

	dst := *c
	dst.IssuesList = slices.Clone(c.IssuesList)
	dst.SchedBlame.TargetQos = slices.Clone(c.SchedBlame.TargetQos)
	for i := range dst.IssuesList {
		dst.IssuesList[i] = slices.Clone(c.IssuesList[i])
	}
	if c.CPUIdle.Filter != nil {
		filter := *c.CPUIdle.Filter
		filter.Included = matcher.CloneRules(c.CPUIdle.Filter.Included)
		filter.Excluded = matcher.CloneRules(c.CPUIdle.Filter.Excluded)
		dst.CPUIdle.Filter = &filter
	}
	return &dst
}
