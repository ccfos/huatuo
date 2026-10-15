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

	"github.com/ccfos/huatuo/internal/profiling/aggregator"
	"github.com/ccfos/huatuo/pkg/profiling"
)

// ProfilerMeta binds a fresh collector and its aggregation policy for one task.
type ProfilerMeta struct {
	ToolName       string
	Type           profiling.Type
	Implementation profiling.Implementation
	Description    string

	Impl            Profiler
	AggregationMode aggregator.AggregationMode
	Aggregator      aggregator.Aggregator
}

// Profiler is the sampling lifecycle. Aggregator ownership stays with Run
// so streaming (eBPF, async-profiler tail) and one-shot (py-spy) providers
// share one shape. Streaming providers initialize their event inputs in Start,
// so ReadDataLoop only consumes data. ReadDataLoop returns when ctx is canceled
// or sampling completes naturally. Streaming providers return nil after a clean
// requested stop; incomplete one-shot results and collection failures return an
// error. Run propagates every loop error and handles external cancellation
// separately, even when the loop stops cleanly.
type Profiler interface {
	Start(ctx context.Context) error
	ReadDataLoop(ctx context.Context, enqueue func(any)) error
	Stop(ctx context.Context) error
}
