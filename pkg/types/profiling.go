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

package types

import profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

// ProfilingToolName identifies profiler Toolstream sessions.
const ProfilingToolName = "profiler"

// ProfilingWindow contains one aggregation window emitted by the profiler.
type ProfilingWindow struct {
	ContainerID              string             `json:"container_id,omitempty"`
	ProfileType              string             `json:"profile_type"`
	Profile                  *profilev1.Profile `json:"profile"`
	AggregationOverflowCount int                `json:"aggr_overflow_count,omitempty"`
}
