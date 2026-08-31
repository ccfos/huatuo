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
	"slices"
	"sync/atomic"

	"github.com/ccfos/huatuo/internal/matcher"
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

// IRQTracingConfig holds irq spike tracing configuration.
type IRQTracingConfig struct {
	Interval              int64 `default:"2"`
	RunTracingToolTimeout int64 `default:"3"`
	IntervalTracing       int64 `default:"300"`
	// Spike rule: fires when >= SpikeMinCPUs cpus simultaneously rise by
	// >= SpikeAbsDeltaThreshold percentage points and >=
	// SpikeRelIncreasePct% relatively.
	SpikeMinCPUs           int   `default:"3"`
	SpikeAbsDeltaThreshold int64 `default:"20"`
	SpikeRelIncreasePct    int64 `default:"30"`
	// Sustained rule: fires when a single cpu's irq+softirq util stays
	// >= SustainedUtilThreshold for SustainedConsecutiveIntervals
	// consecutive samples.
	SustainedConsecutiveIntervals int64 `default:"10"`
	SustainedUtilThreshold        int64 `default:"80"`
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

	IrqTracing IRQTracingConfig

	MemoryBurst MemBurstConfig

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

// Validate rejects invalid autotracing settings. cmd/huatuo-bamai/config calls
// it inside the atomic Load/UpdateAndSync validation transaction so invalid
// values are never acknowledged, published, or persisted; the tracer factories
// re-check their own sections at construction time as defense in depth.
//
// Validation is unconditional: an all-zero section is invalid too, because a
// persisted zero config makes the next daemon start fail in the factory. The
// top-level validator may skip this check when it can prove the tracer is
// blacklisted (its factory never runs), which is the only sanctioned way to
// leave a section unconfigured.
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}

	if err := validateIrqTracingConfig(c.IrqTracing); err != nil {
		return fmt.Errorf("irq tracing: %w", err)
	}

	return nil
}

// Clone returns a deep copy suitable for immutable publication.
func (c *Config) Clone() *Config {
	if c == nil {
		return &Config{}
	}

	dst := *c
	dst.IssuesList = slices.Clone(c.IssuesList)
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
