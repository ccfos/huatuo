// Copyright 2025-2026 The HuaTuo Authors
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
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
	flamegraphtui "github.com/ccfos/huatuo/internal/profiling/flamegraph/tui"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"

	ingestv1 "github.com/grafana/pyroscope/api/gen/proto/go/ingester/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	phlaremodel "github.com/grafana/pyroscope/pkg/model"
)

// FlameData is flamegraph data
var FlameData []flamegraph.FrameData

const perfStackDepth = 20

type eventdata struct {
	Ustack     [perfStackDepth]uint64
	Kstack     [perfStackDepth]uint64
	UstackSize int64
	KstackSize int64
	Pid        uint32
	Name       [16]byte
}

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

// walkPerfStacks visits root-first stacks. The visitor borrows frames only
// for the duration of the call.
func walkPerfStacks(b bpf.BPF, visit func(frames []string, weight int64) error) error {
	items, err := b.DumpMapByName("counts")
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}

	var keyValuePairs []struct {
		Key   *eventdata
		Value uint64
	}

	for _, v := range items {
		ed := eventdata{}
		var count uint64
		if err := binary.Read(bytes.NewReader(v.Key), binary.LittleEndian, &ed); err != nil {
			return fmt.Errorf("decode counts map key: %w", err)
		}
		if err := binary.Read(bytes.NewReader(v.Value), binary.LittleEndian, &count); err != nil {
			return fmt.Errorf("decode counts map value: %w", err)
		}

		keyValuePairs = append(keyValuePairs, struct {
			Key   *eventdata
			Value uint64
		}{
			Key:   &ed,
			Value: count,
		})
	}
	sort.Slice(keyValuePairs, func(i, j int) bool {
		return keyValuePairs[i].Value < keyValuePairs[j].Value
	})

	u := symbol.NewUsymResolver()
	frames := make([]string, 0, 1+2*perfStackDepth)
	for _, kv := range keyValuePairs {
		frames = append(frames[:0], bytesutil.ToStr(kv.Key.Name[:]))
		if kv.Key.UstackSize > 0 {
			for _, frame := range u.UsymStackStrsReversed(kv.Key.Pid, kv.Key.Ustack[:], int(kv.Key.UstackSize)) {
				if frame != "" {
					frames = append(frames, frame)
				}
			}
		}
		if kv.Key.KstackSize > 0 {
			for _, frame := range symbol.KsymStackStrsReversed(kv.Key.Kstack[:], perfStackDepth) {
				if frame != "" {
					frames = append(frames, frame+"_[k]")
				}
			}
		}
		if err := visit(frames, int64(kv.Value)); err != nil {
			return err
		}
	}
	return nil
}

func runTUI(ctx context.Context, b bpf.BPF) error {
	tree := flamegraphtui.NewStackTree(ctx)
	path := []string{"total"}
	if err := walkPerfStacks(b, func(frames []string, weight int64) error {
		path = append(path[:1], frames...)
		return tree.Add(path, weight)
	}); err != nil {
		return fmt.Errorf("read perf stacks: %w", err)
	}

	model, err := tree.BuildModel()
	if err != nil {
		return fmt.Errorf("prepare perf TUI: %w", err)
	}

	_, err = flamegraphtui.Run(ctx, model)
	return err
}

func buildFlameData(b bpf.BPF) ([]flamegraph.FrameData, error) {
	var stacktraces []*ingestv1.StacktraceSample
	var functionNames []string
	err := walkPerfStacks(b, func(frames []string, weight int64) error {
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
	convertedLevels := convertLevels(flame.Levels)
	tree := flamegraph.LevelsToTree(convertedLevels, flame.Names)
	frame, label := flamegraph.TreeToNestedSetDataFrame(tree, "")

	level := *frame.Fields[0]
	value := *frame.Fields[1]
	self := *frame.Fields[2]
	labelf := *frame.Fields[3]

	var (
		levelarr []int64
		valuearr []int64
		selfarr  []int64
		labelarr []string
	)

	for i := 0; i < level.Len(); i++ {
		levelarr = append(levelarr, level.At(i).(int64))
	}

	for i := 0; i < value.Len(); i++ {
		valuearr = append(valuearr, value.At(i).(int64))
	}

	for i := 0; i < self.Len(); i++ {
		selfarr = append(selfarr, self.At(i).(int64))
	}

	labelVmp := label.GetValuesMap()
	keys := make([]string, len(labelVmp))
	for k, v := range labelVmp {
		keys[v] = k
	}

	for i := 0; i < labelf.Len(); i++ {
		formattedNum := fmt.Sprintf("%d", labelf.At(i))
		number, _ := strconv.ParseInt(formattedNum, 10, 64)
		labelarr = append(labelarr, keys[number])
	}

	dataSize := len(levelarr)

	if len(valuearr) != dataSize || len(selfarr) != dataSize || len(labelarr) != dataSize {
		return nil, fmt.Errorf("data length is not equal")
	}

	FlameData = make([]flamegraph.FrameData, dataSize)
	for i := 0; i < dataSize; i++ {
		FlameData[i] = flamegraph.FrameData{
			Level: levelarr[i],
			Value: valuearr[i],
			Self:  selfarr[i],
			Label: labelarr[i],
		}
	}

	return FlameData, nil
}

func writeFlameDataJSON(w io.Writer, data []flamegraph.FrameData) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("JSON encoding error: %w", err)
	}
	_, err = fmt.Fprintln(w, string(jsonData))
	return err
}
