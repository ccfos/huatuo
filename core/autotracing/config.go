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

import "huatuo-bamai/internal/matcher"

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

	// IssuesList for known issue filtering
	IssuesList [][]string
}

var cfg = &Config{}

// Set sets the autotracing config. A nil argument resets to the zero value so
// callers never need to nil-check cfg.
func Set(c *Config) {
	if c == nil {
		cfg = &Config{}
		return
	}
	cfg = c
}
