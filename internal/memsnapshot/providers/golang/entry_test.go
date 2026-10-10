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

package golang

import (
	"context"
	"debug/gosym"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestBuildEntries(t *testing.T) {
	table := &gosym.Table{Funcs: []gosym.Func{
		{Entry: 0x100, End: 0x200, Sym: &gosym.Sym{Name: "runtime.alloc"}},
		{Entry: 0x200, End: 0x300, Sym: &gosym.Sym{Name: "main.allocate"}},
	}}
	for i := range table.Funcs {
		table.Funcs[i].Obj = &gosym.Obj{}
		table.Funcs[i].LineTable = &gosym.LineTable{}
	}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		raw := make([]byte, 40)
		order.PutUint64(raw, 0x1200)
		order.PutUint64(raw[8:], 0x1201)
		order.PutUint64(raw[24:], 0x9999)
		input := []allocation{{key: string(raw), inuseBytes: 256, inuseObjects: 2}}
		entries, err := buildEntries(t.Context(), input, order, &symbolizer{table: table, loadBias: 0x1000})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"runtime.alloc", "main.allocate", "0x0", "0x9999", "0x0"}
		if len(entries) != 1 || entries[0].Name != "main.allocate" || entries[0].AverageBytes != 128 || !reflect.DeepEqual(entries[0].Stack, want) {
			t.Fatalf("entries = %+v", entries)
		}
	}
}

func TestBuildEntriesSourceLocations(t *testing.T) {
	for _, test := range []struct {
		name     string
		function string
		file     string
		line     int
		wantName string
		want     string
	}{
		{
			name: "source_location", function: "main.allocate", file: "example/cache.go", line: 123,
			wantName: "main.allocate", want: "main.allocate, example/cache.go:123",
		},
		{
			name: "commas", function: "main.allocate[go.shape.int,go.shape.string]",
			file: "example/source, files/cache.go", line: 123,
			wantName: "main.allocate[go.shape.int,go.shape.string]",
			want:     "main.allocate[go.shape.int,go.shape.string], example/source, files/cache.go:123",
		},
		{
			name: "missing_file", function: "main.allocate", line: 123,
			wantName: "main.allocate", want: "main.allocate",
		},
		{
			name: "missing_line", function: "main.allocate", file: "example/cache.go",
			wantName: "main.allocate", want: "main.allocate",
		},
		{
			name: "runtime_only", function: "runtime.main", file: "runtime/proc.go", line: 123,
			wantName: "runtime.alloc", want: "runtime.main, runtime/proc.go:123",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			table := &gosym.Table{Funcs: []gosym.Func{
				{
					Entry: 0x100, End: 0x200, Sym: &gosym.Sym{Name: "runtime.alloc"},
					Obj: &gosym.Obj{}, LineTable: &gosym.LineTable{},
				},
				{
					Entry: 0x200, End: 0x300, Sym: &gosym.Sym{Name: "internal/runtime/maps.newTable"},
					Obj: &gosym.Obj{}, LineTable: &gosym.LineTable{},
				},
				{
					Entry: 0x300, End: 0x400, Sym: &gosym.Sym{Name: test.function},
					Obj:       &gosym.Obj{Paths: []gosym.Sym{{Name: test.file, Value: 1}}},
					LineTable: &gosym.LineTable{Line: test.line},
				},
			}}
			var raw [3 * programCounterBytes]byte
			binary.LittleEndian.PutUint64(raw[:], 0x1200)
			binary.LittleEndian.PutUint64(raw[programCounterBytes:], 0x1300)
			binary.LittleEndian.PutUint64(raw[2*programCounterBytes:], 0x1301)
			input := []allocation{{key: string(raw[:]), inuseBytes: 256, inuseObjects: 2}}
			entries, err := buildEntries(t.Context(), input, binary.LittleEndian,
				&symbolizer{table: table, loadBias: 0x1000})
			if err != nil {
				t.Fatal(err)
			}
			want := []memsnapshot.Entry{{
				Kind: "inuse_space_objects", Name: test.wantName,
				Bytes: 256, Objects: 2, AverageBytes: 128,
				Stack: []string{"runtime.alloc", "internal/runtime/maps.newTable", test.want},
			}}
			if !reflect.DeepEqual(entries, want) {
				t.Fatalf("entries = %+v, want %+v", entries, want)
			}
		})
	}
}

func TestBuildEntriesWithoutSymbols(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(order.String(), func(t *testing.T) {
			var raw [5 * programCounterBytes]byte
			order.PutUint64(raw[programCounterBytes:], 0x1200)
			order.PutUint64(raw[2*programCounterBytes:], 0x9999)
			order.PutUint64(raw[4*programCounterBytes:], 0xdead)
			input := []allocation{{key: string(raw[:]), inuseBytes: 256, inuseObjects: 2}}
			want := []memsnapshot.Entry{{
				Kind: "inuse_space_objects", Name: "0x0",
				Bytes: 256, Objects: 2, AverageBytes: 128,
				Stack: []string{"0x0", "0x1200", "0x9999", "0x0", "0xdead"},
			}}
			for _, test := range []struct {
				name    string
				symbols *symbolizer
			}{
				{name: "nil"},
				{name: "missing_table", symbols: &symbolizer{loadBias: 0x1000}},
			} {
				t.Run(test.name, func(t *testing.T) {
					if got, err := buildEntries(t.Context(), input, order, test.symbols); err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("entries without symbols = %+v, %v; want %+v", got, err, want)
					}
				})
			}
		})
	}
}

func TestBuildEntriesCancellation(t *testing.T) {
	var raw [programCounterBytes]byte
	binary.LittleEndian.PutUint64(raw[:], 0x1200)
	for _, input := range [][]allocation{nil, {{key: string(raw[:]), inuseBytes: 128, inuseObjects: 1}}} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("entries=%d/expired=%t", len(input), expired), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				want := context.Canceled
				if expired {
					cancel()
					ctx, cancel = context.WithDeadline(t.Context(), time.Unix(1, 0))
					want = context.DeadlineExceeded
				}
				cancel()
				if got, err := buildEntries(ctx, input, binary.LittleEndian, nil); got != nil || !errors.Is(err, want) {
					t.Fatalf("canceled entries = %+v, %v; want no entries and %v", got, err, want)
				}
			})
		}
	}
}

func TestBuildEntriesCancellationDuringDecode(t *testing.T) {
	var raw [programCounterBytes]byte
	binary.LittleEndian.PutUint64(raw[:], 0x1200)
	input := []allocation{{key: string(raw[:]), inuseBytes: 128, inuseObjects: 1}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	order := cancelingByteOrder{ByteOrder: binary.LittleEndian, cancel: cancel}
	if got, err := buildEntries(ctx, input, order, nil); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled decode = %+v, %v; want no entries and cancellation", got, err)
	}
}

// Cancel at the decoding boundary to avoid depending on scheduler timing.
type cancelingByteOrder struct {
	binary.ByteOrder
	cancel context.CancelFunc
}

func (o cancelingByteOrder) Uint64(raw []byte) uint64 {
	o.cancel()
	return o.ByteOrder.Uint64(raw)
}

func TestBuildEntriesFrameBudget(t *testing.T) {
	// Every single stack fits; their combined display strings must not.
	function := strings.Repeat("f", 4096)
	table := &gosym.Table{Funcs: []gosym.Func{{Entry: 0x100, End: 0x200, Sym: &gosym.Sym{Name: function}, Obj: &gosym.Obj{}, LineTable: &gosym.LineTable{}}}}
	var raw [programCounterBytes]byte
	binary.LittleEndian.PutUint64(raw[:], 0x101)
	input := make([]allocation, maxGoFrameBytes/(2*len(function))+1)
	for i := range input {
		input[i] = allocation{key: string(raw[:]), inuseBytes: 128, inuseObjects: 1}
	}
	if entries, err := buildEntries(t.Context(), input, binary.LittleEndian, &symbolizer{table: table}); err == nil || entries != nil {
		t.Fatalf("aggregate frame budget = %v, %v", entries, err)
	}
}

func TestResolveStackBudgetAndCancellation(t *testing.T) {
	var raw [2 * programCounterBytes]byte
	binary.LittleEndian.PutUint64(raw[:], 0x1234)
	binary.LittleEndian.PutUint64(raw[programCounterBytes:], 0x5678)
	for _, remaining := range []int{0, 52, 104} {
		budget := remaining
		_, frames, err := (*symbolizer)(nil).resolveStack(t.Context(), raw[:], binary.LittleEndian, &budget)
		if (err == nil) != (remaining == 104) {
			t.Fatalf("budget %d: frames=%v error=%v", remaining, frames, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	budget := maxGoFrameBytes
	order := cancelingByteOrder{ByteOrder: binary.LittleEndian, cancel: cancel}
	if _, _, err := (*symbolizer)(nil).resolveStack(ctx, raw[:], order, &budget); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation within stack = %v", err)
	}
	budget = maxGoFrameBytes
	if _, _, err := (*symbolizer)(nil).resolveStack(t.Context(), raw[:1], binary.LittleEndian, &budget); err == nil {
		t.Fatal("accepted misaligned stack")
	}
}
