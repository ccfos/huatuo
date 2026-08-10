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

import (
	"slices"
	"strings"
	"sync/atomic"
)

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
		ExcludedHostNetnamespace bool   `default:"true"`
		ExcludedContainerQos     []string
	}

	Dropwatch struct {
		Filter             string `default:"tcp"`
		MaxEventsPerSecond uint64 `default:"100"`
		ExcludeContainers  []string
	}

	TCPRetransmit struct {
		Filter                     string `default:""`
		EnableTLP                  bool   `default:"false"`
		EnableDropwatchCorrelation bool   `default:"false"`
		MaxEventsPerSecond         uint64 `default:"100"`
	}

	Netdev struct {
		DeviceList []string
	}

	Ras struct {
		MceThrBackoff int64 `default:"1800"`
	}

	IssuesList [][]string
}

var currentConfig atomic.Pointer[Config]

func init() {
	currentConfig.Store(&Config{})
}

// Set atomically publishes an immutable copy of the events config. A nil
// argument resets it to the zero value.
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
	dst.NetRxLatency.ExcludedContainerQos = slices.Clone(c.NetRxLatency.ExcludedContainerQos)
	dst.Dropwatch.ExcludeContainers = slices.Clone(c.Dropwatch.ExcludeContainers)
	dst.Netdev.DeviceList = slices.Clone(c.Netdev.DeviceList)
	dst.IssuesList = slices.Clone(c.IssuesList)
	for i := range dst.IssuesList {
		dst.IssuesList[i] = slices.Clone(c.IssuesList[i])
	}
	return &dst
}

func effectiveDropwatchFilter(config *Config) string {
	filter := strings.TrimSpace(config.Dropwatch.Filter)
	if filter == "" {
		return "tcp"
	}
	return filter
}

func effectiveTCPRetransmitFilter(config *Config) string {
	filter := strings.TrimSpace(config.TCPRetransmit.Filter)
	if filter == "" && config.TCPRetransmit.EnableDropwatchCorrelation {
		return "tcp"
	}
	return filter
}
