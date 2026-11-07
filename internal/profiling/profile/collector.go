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

package profile

import (
	"context"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/pkg/profiling"
)

// ProfilerMeta binds a fresh collector and its aggregation policy for one task.
type ProfilerMeta struct {
	ToolName       string
	Type           profiling.Type
	Implementation profiling.Implementation
	Description    string

	Impl              Profiler
	AggregationMode   pipeline.AggregationMode
	AggregationConfig pipeline.AggregationConfig
}

// Profiler owns sampling resources; Run owns finalization policy and the pipeline.
// Start receives the task context, which survives normal collection stop but
// is canceled on failure or finalization timeout. ReadDataLoop receives the
// collection context and returns nil on natural completion or normal stop;
// real failures must be returned. Finalize stops production and delivers remaining
// samples under the runner's context. No enqueue may outlive either method.
// Stop releases resources on success and failure, with bounded external cleanup.
type Profiler interface {
	Start(ctx context.Context) error
	ReadDataLoop(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error
	Finalize(ctx context.Context, enqueue func(*pipeline.SampleBatch) error) error
	Stop() error
}
