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

package aggregator

import (
	"time"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

// AggregationMode controls snapshot scheduling and queue backpressure.
type AggregationMode uint8

const (
	// AggregationModeStreaming is the zero-value default. It exports periodically
	// and discards records when the queue is full.
	AggregationModeStreaming AggregationMode = iota
	// AggregationModeOneShot exports at completion and waits for queue space.
	AggregationModeOneShot
)

// PipelineConfig binds collection settings and dependencies for one execution.
// Aggregator and Formatter must be initialized before constructing the pipeline.
type PipelineConfig struct {
	AggregationMode     AggregationMode
	AggregationInterval time.Duration
	TracerID            string
	Aggregator          Aggregator
	Formatter           output.Formatter
	// OnFailure notifies the runner to stop collection after an export failure.
	// It runs on the worker and must not wait for Pipeline.Stop.
	OnFailure func()
}
