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

// Config holds event tracing configuration.
type Config struct {
	Softirq struct {
		// 10ms
		DisabledThreshold uint64 `default:"10000000"`
	}

	MemoryReclaim struct {
		// 900ms
		BlockedThreshold uint64 `default:"900000000"`
	}

	NetRxLatency struct {
		Driver2NetRx             uint64 `default:"5"`
		Driver2TCP               uint64 `default:"10"`
		Driver2Userspace         uint64 `default:"115"`
		Driver2Iptable           uint64 `default:"10"`
		ExcludedHostNetnamespace bool   `default:"true"`
		ExcludedContainerQos     []string
	}

	NetTxLatency struct {
		Sendmsg2Qdisc            uint64 `default:"50"`
		Qdisc2Nic                uint64 `default:"1"`
		ExcludedHostNetnamespace bool   `default:"true"`
		ExcludedContainerQos     []string
	}

	Dropwatch struct {
		Filter             string `default:"tcp"`
		MaxEventsPerSecond uint64 `default:"100"`
		ExcludeContainers  []string
	}

	Netdev struct {
		DeviceList []string
	}

	Ras struct {
		MceThrBackoff int64 `default:"1800"`
	}

	IssuesList [][]string
}

var cfg = &Config{}

// Set sets the events config. A nil argument resets to the zero value so
// callers never need to nil-check cfg.
func Set(c *Config) {
	if c == nil {
		cfg = &Config{}
		return
	}
	cfg = c
}
