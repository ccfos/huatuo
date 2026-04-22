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

// Config holds metric collector configuration used by the package at runtime.
type Config struct {
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

var cfg = &Config{}

// SetConfig updates the package level config.
func SetConfig(c *Config) {
	if c == nil {
		cfg = &Config{}
		return
	}
	cfg = c
}
