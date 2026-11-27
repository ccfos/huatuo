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

package main

import (
	"fmt"
	"slices"

	ingestv1 "github.com/grafana/pyroscope/api/gen/proto/go/ingester/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	phlaremodel "github.com/grafana/pyroscope/pkg/model"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
)

func convertLevels(levels []*querierv1.Level) []*flamegraph.Level {
	var result []*flamegraph.Level
	for _, l := range levels {
		newLevel := &flamegraph.Level{
			Values: l.Values,
		}
		result = append(result, newLevel)
	}
	return result
}

func findOrAdd(strA string, b []string) (int, []string) {
	var index int
	found := false
	for idxB, strB := range b {
		if strA == strB {
			index = idxB
			found = true
			break
		}
	}
	if !found {
		b = append(b, strA)
		index = len(b) - 1
	}

	return index, b
}

func buildFlameData(b bpf.BPF) ([]flamegraph.FrameData, error) {
	var stacktraces []*ingestv1.StacktraceSample
	var functionNames []string
	err := readAndVisitPerfStacks(b, func(frames []string, weight int64) error {
		sample := &ingestv1.StacktraceSample{Value: weight}
		for _, frame := range slices.Backward(frames) {
			var index int
			index, functionNames = findOrAdd(frame, functionNames)
			sample.FunctionIds = append(sample.FunctionIds, int32(index))
		}
		stacktraces = append(stacktraces, sample)
		return nil
	})
	if err != nil || len(stacktraces) == 0 {
		return nil, err
	}

	// Convert data formats
	m := phlaremodel.NewTreeMerger()
	sm := phlaremodel.NewStackTraceMerger()

	sm.MergeStackTraces(stacktraces, functionNames)
	if sm.Size() > 0 {
		if err := m.MergeTreeBytes(sm.TreeBytes(-1)); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("phlaremodel: error parsing stack data")
	}

	flame := phlaremodel.NewFlameGraph(m.Tree(), -1)
	tree := flamegraph.LevelsToTree(convertLevels(flame.Levels), flame.Names)
	return flamegraph.TreeToFrameData(tree), nil
}
