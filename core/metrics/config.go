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

package collector

import (
	"fmt"
	"regexp"
	"slices"
	"sync/atomic"
)

// Config holds metric collector configuration used by the package at runtime.
type Config struct {
	AscendNPU struct {
		EnableDCMI bool `default:"true"`
		EnablePCIe bool `default:"false"`
		EnableHCCN bool `default:"false"`
	}

	Mthreads struct {
		EnableHealth bool `default:"true"`
		EnablePCIe   bool `default:"false"`
		EnableMTLink bool `default:"false"`
	}

	NetdevStats struct {
		EnableNetlink  bool `default:"false"`
		DeviceExcluded string
		DeviceIncluded string
	}

	NetdevDCB struct {
		DeviceList []string
	}

	NetdevHW struct {
		DeviceList []string
	}

	Qdisc struct {
		DeviceExcluded string
		DeviceIncluded string
	}

	Vmstat struct {
		IncludedOnHost      string
		ExcludedOnHost      string
		IncludedOnContainer string
		ExcludedOnContainer string
	}

	MemoryEvents struct {
		Included string
		Excluded string
	}

	Netstat struct {
		Included string
		Excluded string
	}

	MountPointStat struct {
		MountPointsIncluded string
	}
}

var currentConfig atomic.Pointer[Config]

func init() {
	currentConfig.Store(&Config{})
}

// Set atomically publishes an immutable copy of the metric collector config.
func Set(c *Config) {
	currentConfig.Store(c.Clone())
}

func configSnapshot() *Config {
	return currentConfig.Load()
}

// Clone returns a deep copy suitable for immutable publication.
func (c *Config) Clone() *Config {
	if c == nil {
		return &Config{}
	}

	dst := *c
	dst.NetdevDCB.DeviceList = slices.Clone(c.NetdevDCB.DeviceList)
	dst.NetdevHW.DeviceList = slices.Clone(c.NetdevHW.DeviceList)
	return &dst
}

// Validate rejects filter patterns that collectors would only fail to compile
// at scrape time, so an invalid configuration is refused before publication.
// An empty pattern is valid: it disables filtering in that direction, the same
// semantics matcher.NewValueMatcher applies at collection.
func (c *Config) Validate() error {
	fields := []struct {
		path    string
		pattern string
	}{
		{"NetdevStats.DeviceIncluded", c.NetdevStats.DeviceIncluded},
		{"NetdevStats.DeviceExcluded", c.NetdevStats.DeviceExcluded},
		{"Qdisc.DeviceIncluded", c.Qdisc.DeviceIncluded},
		{"Qdisc.DeviceExcluded", c.Qdisc.DeviceExcluded},
		{"Vmstat.IncludedOnHost", c.Vmstat.IncludedOnHost},
		{"Vmstat.ExcludedOnHost", c.Vmstat.ExcludedOnHost},
		{"Vmstat.IncludedOnContainer", c.Vmstat.IncludedOnContainer},
		{"Vmstat.ExcludedOnContainer", c.Vmstat.ExcludedOnContainer},
		{"MemoryEvents.Included", c.MemoryEvents.Included},
		{"MemoryEvents.Excluded", c.MemoryEvents.Excluded},
		{"Netstat.Included", c.Netstat.Included},
		{"Netstat.Excluded", c.Netstat.Excluded},
		{"MountPointStat.MountPointsIncluded", c.MountPointStat.MountPointsIncluded},
	}

	for _, f := range fields {
		if f.pattern == "" {
			continue
		}
		if _, err := regexp.Compile(f.pattern); err != nil {
			return fmt.Errorf("invalid pattern %q for %s: %w", f.pattern, f.path, err)
		}
	}

	return nil
}
