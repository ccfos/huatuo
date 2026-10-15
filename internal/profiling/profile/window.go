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

import "github.com/ccfos/huatuo/pkg/types"

// NewWindow owns a copy so window timestamps and labels cannot mutate parser data
// or another snapshot of the same collection.
func NewWindow(data *Data, containerID string) *types.ProfilingWindow {
	p := data.Profile.CloneVT()
	// Pipeline owns the aggregation interval, independently of parser metadata.
	p.TimeNanos = 0
	p.DurationNanos = 0
	return &types.ProfilingWindow{ContainerID: containerID, ProfileType: data.ProfileType, Profile: p}
}
