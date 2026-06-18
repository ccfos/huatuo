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

import pcontext "huatuo-bamai/internal/profiler/context"

// Aggregator absorbs profiler records into language-specific aggregated
// state and exports the result on demand. Each profiler language provides
// its own implementation.
type Aggregator interface {
	// Ingest merges a single record into internal state.
	Ingest(rec any)

	// Snapshot returns the current aggregated result without modifying state.
	// Pipeline calls Reset only after successful output.
	Snapshot(pctx *pcontext.ProfilerContext) (any, error)

	// Reset clears accumulated state for the next cycle.
	Reset()
}
