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

package pipeline

import (
	"fmt"
	"time"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/window"
)

type AggregationMode uint8

const (
	// Streaming is the established default and emits periodic windows.
	AggregationModeStreaming AggregationMode = iota
	AggregationModeOneShot
)

const (
	MaxBatchSamples   = 256
	MaxBatchTextBytes = 1 << 20
	MaxSampleFrames   = 4096
	MaxSampleLabels   = 64
)

type AggregationConfig struct {
	ContainerID string
	ProfileType string
}

type PipelineConfig struct {
	AggregationConfig
	AggregationMode     AggregationMode
	AggregationInterval time.Duration
	TracerID            string
	Formatter           output.Formatter
}

// Validate establishes fixed configuration before any collector starts.
func (c *PipelineConfig) Validate() error {
	if _, err := window.Metadata(c.ProfileType); err != nil {
		return err
	}

	if c.AggregationMode != AggregationModeStreaming && c.AggregationMode != AggregationModeOneShot {
		return fmt.Errorf("unsupported profiling aggregation mode %d", c.AggregationMode)
	}

	if c.Formatter == nil {
		return fmt.Errorf("profiling formatter is required")
	}

	if c.AggregationInterval < 0 {
		return fmt.Errorf("profiling aggregation interval must not be negative")
	}

	return nil
}
