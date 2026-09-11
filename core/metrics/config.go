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
	"math"
	"slices"
	"sync/atomic"
	"time"
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

	Loadavg struct {
		Interval int64 `default:"15"`
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

// Validate rejects invalid sampling intervals before configuration is published.
func (c *Config) Validate() error {
	// The three-interval expiry must also fit in time.Duration.
	const maxIntervalSeconds = math.MaxInt64 / int64(time.Second) / 3
	if c.Loadavg.Interval < 0 || c.Loadavg.Interval > maxIntervalSeconds {
		return fmt.Errorf("loadavg interval must be between 0 and %d seconds (0 uses the default)", maxIntervalSeconds)
	}
	return nil
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
