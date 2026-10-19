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

package java

import (
	"math"
	"sort"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

type classSample struct {
	count uint64
	bytes uint64
}

type sampleStats struct {
	ordinarySampledBytes uint64
	ordinary             map[uint64]classSample
	humongous            map[uint64]classSample
}

func sortObjects(objects []memsnapshot.ObjectAggregate) {
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].ShallowBytes != objects[j].ShallowBytes {
			return objects[i].ShallowBytes > objects[j].ShallowBytes
		}
		if objects[i].Count != objects[j].Count {
			return objects[i].Count > objects[j].Count
		}
		return objects[i].TypeName < objects[j].TypeName
	})
}

func addClassSample(samples map[uint64]classSample, classAddress,
	count, bytes uint64,
) {
	sample := samples[classAddress]
	sample.count = memsnapshot.SaturatingAdd(sample.count, count)
	sample.bytes = memsnapshot.SaturatingAdd(sample.bytes, bytes)
	samples[classAddress] = sample
}

func estimateAggregates(classes map[uint64]*klass, statistics sampleStats,
	ordinaryBytes uint64,
) map[string]memsnapshot.ObjectAggregate {
	aggregates := make(map[string]memsnapshot.ObjectAggregate,
		len(statistics.ordinary)+len(statistics.humongous))
	className := func(classAddress uint64) (string, bool) {
		class := classes[classAddress]
		if class == nil {
			return "", false
		}
		return normalizeClassName(class.name), true
	}
	add := func(name string, count, bytes uint64) {
		aggregate := aggregates[name]
		aggregate.TypeName = name
		aggregate.Count = memsnapshot.SaturatingAdd(aggregate.Count, count)
		aggregate.ShallowBytes = memsnapshot.SaturatingAdd(aggregate.ShallowBytes, bytes)
		aggregates[name] = aggregate
	}
	for classAddress, sample := range statistics.ordinary {
		name, valid := className(classAddress)
		if !valid {
			continue
		}
		add(name,
			scaleEstimate(sample.count, ordinaryBytes,
				statistics.ordinarySampledBytes),
			scaleEstimate(sample.bytes, ordinaryBytes,
				statistics.ordinarySampledBytes))
	}
	for classAddress, sample := range statistics.humongous {
		name, valid := className(classAddress)
		if !valid {
			continue
		}
		add(name, sample.count, sample.bytes)
	}
	return aggregates
}

func scaleEstimate(observed, totalUsed, sampledUsed uint64) uint64 {
	if observed == 0 || totalUsed == 0 || sampledUsed == 0 {
		return observed
	}
	estimate := float64(observed) * float64(totalUsed) / float64(sampledUsed)
	result := clampFloatToUint64(math.Round(estimate))
	if result < observed {
		return observed
	}
	return result
}

func clampFloatToUint64(value float64) uint64 {
	if value <= 0 || math.IsNaN(value) {
		return 0
	}
	if value >= float64(^uint64(0)) || math.IsInf(value, 1) {
		return ^uint64(0)
	}
	return uint64(value)
}
