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
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

// Aggregator absorbs profiler records into language-specific aggregated
// state and exports the result on demand. Each profiler language provides
// its own implementation.
type Aggregator interface {
	// Aggregate incorporates a single record into internal state.
	Aggregate(rec any)

	// Snapshot returns the pprof profile data for upload backends.
	// For raw/flamegraph/svg output, returns nil — the pipeline reads
	// the output formatter directly via OutputFormatter.
	Snapshot(pctx *pcontext.ProfilerContext) (any, error)

	// Reset clears accumulated state for the next cycle.
	Reset()

	// OutputFormatter returns the formatter for file output (raw, flamegraph, svg).
	OutputFormatter() output.Formatter
}

// NewFormatterForOutput creates a Formatter based on the context's output format.
// Returns nil for upload formats — the aggregator should use Snapshot instead.
func NewFormatterForOutput(pctx *pcontext.ProfilerContext) (output.Formatter, error) {
	if pctx.OutputFormat.IsUpload() {
		return nil, nil
	}

	return pctx.OutputFormat.NewFormatter()
}
