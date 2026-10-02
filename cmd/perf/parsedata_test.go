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
	"errors"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf"
)

type countsMapBPF struct {
	bpf.BPF
	items []bpf.MapItem
	err   error
}

func (b countsMapBPF) DumpMapByName(name string) ([]bpf.MapItem, error) {
	if name != "counts" {
		return nil, errors.New("unexpected BPF map name")
	}
	return b.items, b.err
}

func TestBuildFlameDataEmptyCounts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		items []bpf.MapItem
	}{
		{name: "nil map results"},
		{name: "empty map results", items: []bpf.MapItem{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := buildFlameData(countsMapBPF{items: tc.items})
			if err != nil {
				t.Fatalf("build flame data: %v", err)
			}
			if data == nil || len(data) != 0 {
				t.Fatalf("empty counts returned %#v; want a non-nil empty slice", data)
			}

			var output bytes.Buffer
			if err := writeFlameDataJSON(&output, data); err != nil {
				t.Fatalf("write flame data: %v", err)
			}
			if got := output.String(); got != "[]\n" {
				t.Fatalf("JSON output = %q; want %q", got, "[]\n")
			}
		})
	}
}

func TestBuildFlameDataCountsError(t *testing.T) {
	want := errors.New("read counts")
	data, err := buildFlameData(countsMapBPF{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v; want %v", err, want)
	}
	if data != nil {
		t.Fatalf("data = %#v; want nil on error", data)
	}
}
