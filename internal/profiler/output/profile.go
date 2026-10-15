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
	"context"
	"fmt"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

// ProfileReader borrows one profile while resolving its stacks and strings.
// The caller must not mutate the profile during use.
type ProfileReader struct {
	profile   *profilev1.Profile
	functions map[uint64]*profilev1.Function
	locations map[uint64]*profilev1.Location
}

func NewProfileReader(profile *profilev1.Profile) (*ProfileReader, error) {
	if profile == nil {
		return nil, fmt.Errorf("format profile: profile is required")
	}

	r := &ProfileReader{
		profile:   profile,
		functions: make(map[uint64]*profilev1.Function, len(profile.Function)),
		locations: make(map[uint64]*profilev1.Location, len(profile.Location)),
	}
	for _, function := range profile.Function {
		if function == nil || function.Id == 0 || r.functions[function.Id] != nil {
			return nil, fmt.Errorf("format profile: function ids must be nonzero and unique")
		}

		r.functions[function.Id] = function
	}

	for _, location := range profile.Location {
		if location == nil || location.Id == 0 || r.locations[location.Id] != nil {
			return nil, fmt.Errorf("format profile: location ids must be nonzero and unique")
		}

		r.locations[location.Id] = location
	}

	return r, nil
}

func (r *ProfileReader) Text(index int64) (string, error) {
	if index < 0 || index >= int64(len(r.profile.StringTable)) {
		return "", fmt.Errorf("format profile: invalid string index %d", index)
	}

	return r.profile.StringTable[index], nil
}

// WalkStack visits caller-first frames, retaining addresses without symbols.
// File and line are empty/zero when no source position is available.
func (r *ProfileReader) WalkStack(sample *profilev1.Sample, visit func(name, file string, line int64) error) error {
	if sample == nil {
		return fmt.Errorf("format profile: sample is required")
	}

	for i := len(sample.LocationId) - 1; i >= 0; i-- {
		id := sample.LocationId[i]
		location := r.locations[id]
		if location == nil {
			return fmt.Errorf("format profile: location %d is missing", id)
		}

		if len(location.Line) == 0 {
			if err := visit(fmt.Sprintf("0x%x", location.Address), "", 0); err != nil {
				return err
			}

			continue
		}

		for j := len(location.Line) - 1; j >= 0; j-- {
			line := location.Line[j]
			if line == nil {
				return fmt.Errorf("format profile: nil line in location %d", id)
			}

			function := r.functions[line.FunctionId]
			if function == nil {
				return fmt.Errorf("format profile: function %d is missing", line.FunctionId)
			}

			name, err := r.Text(function.Name)
			if err != nil {
				return err
			}

			file, err := r.Text(function.Filename)
			if err != nil {
				return err
			}

			if err := visit(name, file, line.Line); err != nil {
				return err
			}
		}
	}

	return nil
}

// WalkProfile visits caller-first stacks with the first sample value, without
// rescaling its unit. The callback may borrow frames only until it returns.
func WalkProfile(ctx context.Context, profile *profilev1.Profile, visit func([]string, int64) error) error {
	reader, err := NewProfileReader(profile)
	if err != nil {
		return err
	}

	for _, sample := range profile.Sample {
		if err := ctx.Err(); err != nil {
			return err
		}

		if sample == nil || len(sample.Value) == 0 {
			return fmt.Errorf("format profile: sample value is required")
		}

		var frames []string
		if err := reader.WalkStack(sample, func(name, _ string, _ int64) error {
			frames = append(frames, name)
			return nil
		}); err != nil {
			return err
		}

		if err := visit(frames, sample.Value[0]); err != nil {
			return err
		}
	}

	return nil
}
