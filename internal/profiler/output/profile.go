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

package output

import (
	"fmt"
	"slices"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

// AddProfile feeds the first sample value into a renderer in caller-first order.
// Values are already in the profile's sample unit and must not be scaled again.
func AddProfile(renderer Renderer, profile *profilev1.Profile) error {
	if profile == nil {
		return fmt.Errorf("format profile: profile is required")
	}
	functions := make(map[uint64]*profilev1.Function, len(profile.Function))
	for _, function := range profile.Function {
		functions[function.Id] = function
	}
	locations := make(map[uint64]*profilev1.Location, len(profile.Location))
	for _, location := range profile.Location {
		locations[location.Id] = location
	}
	for _, sample := range profile.Sample {
		if len(sample.Value) == 0 {
			return fmt.Errorf("format profile: sample value is required")
		}
		var frames []string
		for _, id := range sample.LocationId {
			location := locations[id]
			if location == nil {
				return fmt.Errorf("format profile: location %d is missing", id)
			}
			for _, line := range location.Line {
				function := functions[line.FunctionId]
				if function == nil {
					return fmt.Errorf("format profile: function %d is missing", line.FunctionId)
				}
				if function.Name < 0 || function.Name >= int64(len(profile.StringTable)) {
					return fmt.Errorf("format profile: invalid function name index %d", function.Name)
				}
				frames = append(frames, profile.StringTable[function.Name])
			}
		}
		slices.Reverse(frames)
		if err := renderer.Add(&Sample{Frames: frames, Count: sample.Value[0]}); err != nil {
			return err
		}
	}
	return nil
}
