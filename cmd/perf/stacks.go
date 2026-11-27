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
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
)

const perfStackDepth = 20

type eventdata struct {
	Ustack     [perfStackDepth]uint64
	Kstack     [perfStackDepth]uint64
	UstackSize int64
	KstackSize int64
	Pid        uint32
	Name       [16]byte
}

// readAndVisitPerfStacks visits root-first stacks. The visitor borrows frames only
// for the duration of the call.
func readAndVisitPerfStacks(b bpf.BPF, visit func(frames []string, weight int64) error) error {
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
