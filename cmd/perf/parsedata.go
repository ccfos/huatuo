// Copyright 2025 The HuaTuo Authors
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
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/flamegraph"
	"huatuo-bamai/internal/symbol"
	"huatuo-bamai/internal/utils/bytesutil"

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

// CgDumpTrace is an interface for dump stacks in cgusage case
func CgDumpTrace(addrs []uint64) string {
	stacks := symbol.DumpKernelBackTrace(addrs, perfStackDepth)
	return strings.Join(stacks.BackTrace, "\n")
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

func parsedata(b bpf.BPF) error {
	items, err := b.DumpMapByName("counts")
	if err != nil || items == nil {
		return err
	}

	var keyValuePairs []struct {
		Key   *eventdata
		Value uint64
	}

	u := symbol.NewUsym()
	for _, v := range items {
		ed := eventdata{}
		var count uint64
		buf := bytes.NewReader(v.Key)
		err := binary.Read(buf, binary.LittleEndian, &ed)
		if err != nil {
			return err
		}
		buf = bytes.NewReader(v.Value)
		err = binary.Read(buf, binary.LittleEndian, &count)
		if err != nil {
			return err
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

	var stacktraces []*ingestv1.StacktraceSample
	var functionNames []string

	for k := range keyValuePairs {
		sample := &ingestv1.StacktraceSample{}
		var index int
		kv := keyValuePairs[k]
		sample.Value = int64(kv.Value)

		if kv.Key.KstackSize > 0 {
			kernelStack := CgDumpTrace(kv.Key.Kstack[:])
			kstack := strings.Split(kernelStack, "\n")
			for _, v := range kstack {
				if v != "" {
					index, functionNames = findOrAdd(v+"_[k]", functionNames)
					sample.FunctionIds = append(sample.FunctionIds, int32(index))
				}
			}
		}

		if kv.Key.UstackSize > 0 {
			for _, addr := range &kv.Key.Ustack {
				if addr == 0 {
					break
				}
				usym := u.ResolveUstack(addr, kv.Key.Pid)
				if usym != "" {
					index, functionNames = findOrAdd(usym, functionNames)
					sample.FunctionIds = append(sample.FunctionIds, int32(index))
				}
			}
		}

		sttitle := bytesutil.ToString(kv.Key.Name[:])
		index, functionNames = findOrAdd(sttitle, functionNames)
		sample.FunctionIds = append(sample.FunctionIds, int32(index))

		stacktraces = append(stacktraces, sample)
	}

	// Convert data formats
	m := phlaremodel.NewTreeMerger()
	sm := phlaremodel.NewStackTraceMerger()

	sm.MergeStackTraces(stacktraces, functionNames)
	if sm.Size() > 0 {
		if err := m.MergeTreeBytes(sm.TreeBytes(-1)); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("phlaremodel: Error parsing stack data")
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

	DataSize := len(levelarr)

	if len(valuearr) != DataSize || len(selfarr) != DataSize || len(labelarr) != DataSize {
		return fmt.Errorf("Data length is not equal")
	}

	FlameData = make([]flamegraph.FrameData, DataSize)
	for i := 0; i < DataSize; i++ {
		FlameData[i] = flamegraph.FrameData{
			Level: levelarr[i],
			Value: valuearr[i],
			Self:  selfarr[i],
			Label: labelarr[i],
		}
	}

	// save
	jsonData, err := json.Marshal(FlameData)
	if err != nil {
		return fmt.Errorf("JSON encoding error: %w", err)
	}
	fmt.Println(string(jsonData))
	return err
}
