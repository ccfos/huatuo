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

package events

// Config holds event tracing configuration used by the package at runtime.
type Config struct {
	Softirq struct {
		DisabledThreshold uint64 `default:"10000000"`
	}

	MemoryReclaim struct {
		BlockedThreshold uint64 `default:"900000000"`
	}

	NetRxLatency struct {
		Driver2NetRx             uint64 `default:"5"`
		Driver2TCP               uint64 `default:"10"`
		Driver2Userspace         uint64 `default:"115"`
		ExcludedHostNetnamespace bool   `default:"true"`
		ExcludedContainerQos     []string
	}

	Dropwatch struct {
		ExcludedNeighInvalidate bool `default:"true"`
	}

	Netdev struct {
		DeviceList []string
	}

	PatternList [][]string
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
