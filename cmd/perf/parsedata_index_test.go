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
	"reflect"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
)

func TestFunctionNamesRetainFirstSeenIndices(t *testing.T) {
	var index functionNameIndex
	names := []string{"worker", "leaf_[k]", "worker", "leaf", "leaf_[k]", "", ""}
	wantIDs := []int{0, 1, 0, 2, 1, 3, 3}
	for i, name := range names {
		if got := index.add(name); got != wantIDs[i] {
			t.Fatalf("add(%q)=%d, want %d", name, got, wantIDs[i])
		}
	}
	wantNames := []string{"worker", "leaf_[k]", "leaf", ""}
	if !reflect.DeepEqual(index.names, wantNames) {
		t.Fatalf("names=%v, want %v", index.names, wantNames)
	}
}

func BenchmarkFunctionNameIndex(b *testing.B) {
	for _, count := range []int{100, 10000, 40000} {
		names := make([]string, count)
		for i := range names {
			names[i] = fmt.Sprintf("symbol_%05d", i)
		}
		b.Run(fmt.Sprintf("distinct=%d", count), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				var index functionNameIndex
				for _, name := range names {
					index.add(name)
				}
				if len(index.names) != len(names) {
					b.Fatal(len(index.names))
				}
			}
		})
	}
}

type symbolIndexBPF struct {
	bpf.BPF
	items []bpf.MapItem
}

func (b *symbolIndexBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name != "counts" {
		return nil, fmt.Errorf("unexpected map %q", name)
	}
	return b.items, nil
}

func TestBuildFlameDataCombinesRepeatedCommandNames(t *testing.T) {
	stub := &symbolIndexBPF{}
	for _, entry := range []struct {
		name  string
		count uint64
	}{{"worker", 3}, {"other", 4}, {"worker", 2}} {
		event := eventdata{}
		copy(event.Name[:], entry.name)
		var key, value bytes.Buffer
		if err := binary.Write(&key, binary.LittleEndian, &event); err != nil {
			t.Fatal(err)
		}
		if err := binary.Write(&value, binary.LittleEndian, entry.count); err != nil {
			t.Fatal(err)
		}
		stub.items = append(stub.items, bpf.MapItem{Key: key.Bytes(), Value: value.Bytes()})
	}
	data, err := buildFlameData(stub)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for i := range data {
		got[data[i].Label] = data[i].Value
	}
	want := map[string]int64{"total": 9, "worker": 5, "other": 4}
	if !reflect.DeepEqual(got, want) || len(data) != 3 {
		t.Fatalf("frames=%+v, want totals=%v", data, want)
	}
}
