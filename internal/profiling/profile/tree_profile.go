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
	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

// buildTreeProfile keeps the tree library's stack ordering and metadata without
// constructing its legacy protobuf model. The tree is local to ParseTree.
func buildTreeProfile(tree *ptree.Tree, metadata *ptree.PprofMetadata) *profilev1.Profile {
	p := &profilev1.Profile{
		StringTable:   []string{""},
		Mapping:       []*profilev1.Mapping{{Id: 1}},
		TimeNanos:     metadata.StartTime.UnixNano(),
		DurationNanos: metadata.Duration.Nanoseconds(),
	}
	strings := make(map[string]int64)
	internString := func(value string) int64 {
		if id, ok := strings[value]; ok {
			return id
		}
		id := int64(len(p.StringTable))
		strings[value] = id
		p.StringTable = append(p.StringTable, value)
		return id
	}
	p.SampleType = []*profilev1.ValueType{{Type: internString(metadata.Type), Unit: internString(metadata.Unit)}}
	if metadata.Period != 0 && metadata.PeriodType != "" && metadata.PeriodUnit != "" {
		p.Period = metadata.Period
		p.PeriodType = &profilev1.ValueType{Type: internString(metadata.PeriodType), Unit: internString(metadata.PeriodUnit)}
	}

	// Each symbolic frame has one function and one location, with matching IDs.
	locations := make(map[string]uint64)
	tree.IterateStacks(func(_ string, self uint64, stack []string) {
		sample := &profilev1.Sample{Value: []int64{int64(self)}}
		if len(stack) > 0 {
			sample.LocationId = make([]uint64, 0, len(stack))
		}
		for _, frame := range stack {
			id, ok := locations[frame]
			if !ok {
				id = uint64(len(p.Location) + 1)
				locations[frame] = id
				name := internString(frame)
				p.Function = append(p.Function, &profilev1.Function{Id: id, Name: name, SystemName: name})
				p.Location = append(p.Location, &profilev1.Location{Id: id, MappingId: 1, Line: []*profilev1.Line{{FunctionId: id}}})
			}
			sample.LocationId = append(sample.LocationId, id)
		}
		p.Sample = append(p.Sample, sample)
	})
	return p
}
