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

package window

import (
	"context"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	"github.com/ccfos/huatuo/pkg/types"
)

// BuildProfile trusts internally constructed windows; external callers must
// Validate first. Values are already in their final units, with no period.
func BuildProfile(ctx context.Context, w *types.AggregationWindow) (*profilev1.Profile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	parts, err := Metadata(w.ProfileType)
	if err != nil {
		return nil, err
	}

	a := &w.Aggregated
	p := &profilev1.Profile{
		TimeNanos:     a.TimeNanos,
		DurationNanos: a.DurationNanos,
		StringTable:   []string{""},
	}
	strings := map[string]int64{"": 0}
	intern := func(s string) int64 {
		if id, ok := strings[s]; ok {
			return id
		}

		id := int64(len(p.StringTable))
		strings[s] = id
		p.StringTable = append(p.StringTable, s)

		return id
	}
	p.SampleType = []*profilev1.ValueType{{
		Type: intern(parts[1]),
		Unit: intern(parts[2]),
	}}
	for i := range a.Frames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		f := &a.Frames[i]
		id := uint64(i) + 1
		p.Function = append(p.Function, &profilev1.Function{
			Id:         id,
			Name:       intern(f.Name),
			SystemName: intern(f.Name),
			Filename:   intern(f.File),
		})
		p.Location = append(p.Location, &profilev1.Location{
			Id: id,
			Line: []*profilev1.Line{{
				FunctionId: id,
				Line:       f.Line,
			}},
		})
	}

	var emptyLabelString int64
	p.Sample = make([]*profilev1.Sample, 0, len(a.Samples))
	for i := range a.Samples {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		s := &a.Samples[i]
		sample := &profilev1.Sample{Value: []int64{s.Value}}
		for _, stackID := range [2]uint32{s.KernelStack, s.UserStack} {
			if stackID == 0 {
				continue
			}

			stack := a.Stacks[stackID-1]
			for j := len(stack) - 1; j >= 0; j-- {
				sample.LocationId = append(sample.LocationId, uint64(stack[j])+1)
			}
		}

		for _, label := range Labels(a, s) {
			item := &profilev1.Label{Key: intern(label.Name)}
			if label.Kind == types.ProfileLabelString {
				item.Str = intern(label.StringValue)
				if item.Str == 0 {
					if emptyLabelString == 0 {
						emptyLabelString = int64(len(p.StringTable))
						p.StringTable = append(p.StringTable, "")
					}

					item.Str = emptyLabelString
				}
			} else {
				item.Num = label.NumberValue
				item.NumUnit = intern(label.NumberUnit)
			}

			sample.Label = append(sample.Label, item)
		}

		p.Sample = append(p.Sample, sample)
	}

	return p, nil
}
