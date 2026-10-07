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
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestBuildEntries(t *testing.T) {
	table := &gosym.Table{Funcs: []gosym.Func{
		{Entry: 0x100, End: 0x200, Sym: &gosym.Sym{Name: "runtime.alloc"}},
		{Entry: 0x200, End: 0x300, Sym: &gosym.Sym{Name: "main.allocate"}},
	}}
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

func TestBuildEntriesWithoutSymbols(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(order.String(), func(t *testing.T) {
			var raw [5 * programCounterBytes]byte
			order.PutUint64(raw[programCounterBytes:], 0x1200)
			order.PutUint64(raw[2*programCounterBytes:], 0x9999)
			order.PutUint64(raw[4*programCounterBytes:], 0xdead)
			input := []allocation{{key: string(raw[:]), inuseBytes: 256, inuseObjects: 2}}
			want := []memsnapshot.Entry{{
				Kind: "allocation_site", Name: "0x0",
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
