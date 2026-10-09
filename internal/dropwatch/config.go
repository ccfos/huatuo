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

package dropwatch

import (
	"errors"
	"fmt"
)

// HardwareMode controls devlink trap tracing.
type HardwareMode uint8

const (
	// HardwareAuto enables hardware tracing when the tracepoint is available.
	HardwareAuto HardwareMode = iota
	// HardwareDisabled traces software drops only.
	HardwareDisabled
)

// Config configures one tracing instance. Device lists are mutually exclusive.
type Config struct {
	BPFPath          string
	FilterExpression string
	IncludeDevices   []string
	ExcludeDevices   []string
	// MaxEventsPerSecond is zero for unlimited emission.
	MaxEventsPerSecond uint64
	HardwareMode       HardwareMode
}

func (c *Config) validate() error {
	if c.BPFPath == "" {
		return errors.New("dropwatch: BPF path is required")
	}
	if len(c.IncludeDevices) != 0 && len(c.ExcludeDevices) != 0 {
		return errors.New("dropwatch: include and exclude devices are mutually exclusive")
	}
	switch c.HardwareMode {
	case HardwareAuto, HardwareDisabled:
	default:
		return fmt.Errorf("dropwatch: invalid hardware mode %d", c.HardwareMode)
	}
	return nil
}
