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

package aggregation

import (
	"encoding/binary"
	"hash/maphash"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/pkg/types"
)

func intern(p *Aggregator, names ...string) uint32 {
	values := frames(names...)
	id, hash := p.findStackID(values)
	if id == 0 {
		id = p.insertStack(values, hash)
	}

	return id
}

func TestAggregatorStackLookupOrAdd(t *testing.T) {
	p := New()
	first := intern(p, "main", "work")
	second := intern(p, "main", "work")
	if first == 0 || first != second {
		t.Fatalf("IDs %d %d", first, second)
	}
}

func TestAggregatorStackLookupOrAddEmpty(t *testing.T) {
	p := New()
	if intern(p) != 0 || len(p.profile.Stacks) != 0 {
		t.Fatal("empty stack initialized state")
	}
}

func TestAggregatorStackPreservesFrameBoundaries(t *testing.T) {
	p := New()
	if intern(p, "foo;bar") == intern(p, "foo", "bar") {
		t.Fatal("frame boundaries lost")
	}
}

func TestAggregatorStackResolvesHashCollisions(t *testing.T) {
	p := New()
	first := intern(p, "first")
	_, hash := p.findStackID(frames("first"))
	// Deliberately violate the insertion hash contract to exercise collisions.
	second := p.insertStack(frames("second"), hash)
	repeated, _ := p.findStackID(frames("first"))
	if first == second || repeated != first {
		t.Fatal("collision lost stack identity")
	}

	// Unequal candidates must not turn a hash match into a stack match.
	absent := frames("absent")
	_, absentHash := p.findStackID(absent)
	p.insertStack(frames("third"), absentHash)
	p.insertStack(frames("fourth"), absentHash)
	if id, _ := p.findStackID(absent); id != 0 {
		t.Fatal("hash collision matched an unequal stack")
	}

	p.Drain()
	if id, _ := p.findStackID(frames("first")); id != 0 {
		t.Fatal("Drain retained a collision bucket")
	}
	if id := intern(p, "first"); id != 1 {
		t.Fatalf("new window stack ID=%d, want 1", id)
	}
}

func TestAggregatorStackReset(t *testing.T) {
	p := New()
	intern(p, "main")
	p.Drain()
	if len(p.stackBuckets) != 0 || len(p.profile.Stacks) != 0 {
		t.Fatal("reset retained stacks")
	}

	if intern(p, "work") != 1 {
		t.Fatal("reset did not restart IDs")
	}
}

func TestAggregatorLabelsResolveHashCollisions(t *testing.T) {
	a := New()
	firstLabels := []types.ProfileLabel{types.ProfileStringLabel("comm", "first")}
	secondLabels := []types.ProfileLabel{types.ProfileStringLabel("comm", "second")}
	_, hash := a.findLabelSetID(firstLabels)
	first := a.insertLabelSet(firstLabels, hash)
	second := a.insertLabelSet(secondLabels, hash)
	got, _ := a.findLabelSetID(firstLabels)
	if first == second || got != first {
		t.Fatal("collision lost label identity")
	}

	// Force an unequal candidate under a third input's hash to exercise misses.
	absent := []types.ProfileLabel{types.ProfileStringLabel("comm", "absent")}
	_, absentHash := a.findLabelSetID(absent)
	a.insertLabelSet(secondLabels, absentHash)
	if got, _ := a.findLabelSetID(absent); got != 0 {
		t.Fatal("hash collision matched unequal labels")
	}

	a.Drain()
	if got, _ := a.findLabelSetID(firstLabels); got != 0 {
		t.Fatal("Drain retained a label collision bucket")
	}
	_, hash = a.findLabelSetID(firstLabels)
	if id := a.insertLabelSet(firstLabels, hash); id != 1 {
		t.Fatalf("new window label set ID=%d, want 1", id)
	}
	if id, _ := a.findLabelSetID(firstLabels); id != 1 {
		t.Fatalf("new window lookup ID=%d, want 1", id)
	}
}

func TestStackHashPreservesFrameIdentity(t *testing.T) {
	inputs := [][]types.ProfileFrame{
		{
			{
				Name: "ab",
				File: "c",
			},
		},
		{
			{
				Name: "a",
				File: "bc",
			},
		},
		{
			{
				Name: "ab",
				File: "c",
				Line: -1,
			},
		},
		{
			{
				Name: "ab",
				File: "c",
				Line: math.MinInt64,
			},
		},
		{
			{
				Name: "ab",
				File: "c",
				Line: math.MaxInt64,
			},
		},
		{{Name: "a"}, {Name: "b"}},
		{{Name: "b"}, {Name: "a"}},
		{{}},
		{{}, {}},
		{
			{
				Name: "a\x00b",
				File: "c\x00d",
			},
		},
		{
			{
				Name: strings.Repeat("name", 100),
				File: strings.Repeat("file", 100),
				Line: 42,
			},
		},
	}
	a := New()
	for i, input := range inputs {
		id, hash := a.findStackID(input)
		if id != 0 {
			t.Fatalf("input %d matched a different stack: %d", i, id)
		}

		a.insertStack(input, hash)
	}

	for i, input := range inputs {
		id, _ := a.findStackID(input)
		if id != uint32(i+1) {
			t.Fatalf("input %d: ID = %d, want %d", i, id, i+1)
		}
	}
}

func TestStackHashMatchesEncodedBytes(t *testing.T) {
	for _, textSize := range []int{0, 1, 103, 104, 105, 231, 232, 233, 1024} {
		for _, depth := range []int{1, 5, 8, 32, 64} {
			input := make([]types.ProfileFrame, depth)
			for i := range input {
				input[i] = types.ProfileFrame{
					Name: strings.Repeat("n", textSize),
					Line: int64(i) - 32,
				}
			}

			checkStackHashEncoding(t, input)
		}
	}

	checkStackHashEncoding(t, []types.ProfileFrame{{File: strings.Repeat("file", 300)}})

	for _, prefix := range []int{0, 1, 7, 8, 32} {
		input := make([]types.ProfileFrame, prefix)
		for i := range input {
			input[i].Name = "small"
		}

		input = append(input, types.ProfileFrame{
			Name: strings.Repeat("name", 300),
			File: strings.Repeat("file", 300),
			Line: math.MinInt64,
		}, types.ProfileFrame{
			Name: "tail",
			File: "tail.go",
			Line: math.MaxInt64,
		})
		checkStackHashEncoding(t, input)
	}
}

func checkStackHashEncoding(t *testing.T, frames []types.ProfileFrame) {
	t.Helper()
	var encoded []byte
	for i := range frames {
		f := &frames[i]
		encoded = binary.LittleEndian.AppendUint64(encoded, uint64(len(f.Name)))
		encoded = binary.LittleEndian.AppendUint64(encoded, uint64(len(f.File)))
		encoded = binary.LittleEndian.AppendUint64(encoded, uint64(f.Line))
		encoded = append(encoded, f.Name...)
		encoded = append(encoded, f.File...)
	}

	a := New()
	want := maphash.Bytes(a.hashSeed, encoded)
	id, got := a.findStackID(frames)
	if id != 0 || got != want {
		t.Fatalf("frames=%d bytes=%d: ID=%d hash=%x, want ID=0 hash=%x", len(frames), len(encoded), id, got, want)
	}

	wantID := a.insertStack(frames, got)
	id, got = a.findStackID(frames)
	if id != wantID || got != want {
		t.Fatalf("lookup ID=%d hash=%x, want ID=%d hash=%x", id, got, wantID, want)
	}
}

func TestStackHashDoesNotAllocate(t *testing.T) {
	for _, size := range []int{8, 8 << 10, 128 << 10} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			a := New()
			input := frames(strings.Repeat("x", size))
			if allocations := testing.AllocsPerRun(100, func() { a.findStackID(input) }); allocations != 0 {
				t.Fatalf("stack lookup allocated %g times", allocations)
			}
		})
	}
}

func TestLabelHashPreservesIdentity(t *testing.T) {
	inputs := [][]types.ProfileLabel{
		{types.ProfileStringLabel("ab", "c")},
		{types.ProfileStringLabel("a", "bc")},
		{types.ProfileStringLabel("a", "b")},
		{{Name: "a", Kind: types.ProfileLabelString, NumberUnit: "b"}},
		{{Name: "a", Kind: types.ProfileLabelNumber, NumberValue: -1}},
		{{Name: "a", Kind: types.ProfileLabelNumber, NumberValue: math.MinInt64}},
		{{Name: "a", Kind: types.ProfileLabelNumber, NumberValue: math.MaxInt64}},
		{{Name: "a", Kind: types.ProfileLabelString}},
		{{Name: "a", Kind: types.ProfileLabelNumber}},
		{{}},
		{{}, {}},
		{types.ProfileStringLabel("a", ""), types.ProfileStringLabel("b", "")},
		{types.ProfileStringLabel("b", ""), types.ProfileStringLabel("a", "")},
		{types.ProfileStringLabel("a\x00b", "c\x00d")},
		{types.ProfileStringLabel("long", strings.Repeat("value", 1000))},
	}
	a := New()
	if id, hash := a.findLabelSetID(nil); id != 0 || hash != 0 {
		t.Fatalf("empty labels returned ID=%d hash=%x", id, hash)
	}
	for i, labels := range inputs {
		id, hash := a.findLabelSetID(labels)
		if id != 0 {
			t.Fatalf("input %d matched different labels: %d", i, id)
		}
		a.insertLabelSet(labels, hash)
	}
	for i, labels := range inputs {
		if id, _ := a.findLabelSetID(labels); id != uint32(i+1) {
			t.Fatalf("input %d: ID=%d, want %d", i, id, i+1)
		}
	}
}
