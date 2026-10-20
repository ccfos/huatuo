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
	"math"
	"slices"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

func add(t testing.TB, p *Aggregator, s *Sample) {
	t.Helper()
	if err := p.Add(s); err != nil {
		t.Fatal(err)
	}
}

func frames(names ...string) []types.ProfileFrame {
	result := make([]types.ProfileFrame, len(names))
	for i, name := range names {
		result[i].Name = name
	}

	return result
}

func TestAggregatorAggregatesLockTime(t *testing.T) {
	p := New()
	s := Sample{
		UserFrames: frames("foo", "bar"),
		Labels:     []types.ProfileLabel{types.ProfileLockAddressLabel(0xab)},
		Value:      10,
	}
	add(t, p, &s)
	add(t, p, &s)
	if p.profile.Samples[0].Value != 20 {
		t.Fatal(p.profile)
	}
}

func TestAggregatorAggregatesStackSamplesWithoutMutatingInput(t *testing.T) {
	p := New()
	s := Sample{
		UserFrames:   frames("main", "work"),
		KernelFrames: frames("entry_SYSCALL_64_after_hwframe"),
		Value:        7,
	}
	add(t, p, &s)
	add(t, p, &s)
	if s.Value != 7 || p.profile.Samples[0].Value != 14 {
		t.Fatal(p.profile)
	}
}

func TestAggregatorSeparatesStackSampleIdentity(t *testing.T) {
	for _, field := range []string{"pid", "comm", "category", "user frames", "kernel frames"} {
		t.Run(field, func(t *testing.T) {
			p := New()
			left := Sample{
				UserFrames:   frames("main", "work"),
				KernelFrames: frames("syscall"),
				Labels: []types.ProfileLabel{
					types.ProfileStringLabel("comm", "app"),
					types.ProfileStringLabel("offcpu_category", "unknown"),
					types.ProfilePIDLabel(12),
				},
				Value: 1,
			}
			right := left
			right.Labels = slices.Clone(left.Labels)
			switch field {
			case "pid":
				right.Labels[2] = types.ProfilePIDLabel(13)
			case "comm":
				right.Labels[0] = types.ProfileStringLabel("comm", "worker")
			case "category":
				right.Labels[1] = types.ProfileStringLabel("offcpu_category", "blocked")
			case "user frames":
				right.UserFrames = frames("main", "other")
			case "kernel frames":
				right.KernelFrames = frames("other")
			}

			add(t, p, &left)
			add(t, p, &right)
			if len(p.profile.Samples) != 2 {
				t.Fatal(p.profile)
			}
		})
	}
}

func TestAggregatorDrainResolvesStackTraceIDs(t *testing.T) {
	p := New()
	add(t, p, &Sample{
		UserFrames:   frames("main", "work"),
		KernelFrames: frames("entry_SYSCALL_64_after_hwframe"),
		Labels:       []types.ProfileLabel{types.ProfileStringLabel("comm", "app"), types.ProfilePIDLabel(12)},
		Value:        7,
	})
	got, err := window.BuildProfile(t.Context(), &types.AggregationWindow{
		ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		Aggregated:  p.Drain(),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"app", "12", "main", "work", "entry_SYSCALL_64_after_hwframe_[k]"} {
		if !slices.Contains(got.StringTable, name) {
			t.Fatal(name)
		}
	}

	if len(got.Sample) != 1 || got.Sample[0].Value[0] != 7 {
		t.Fatal(got)
	}
}

func TestAggregatorPhysicalMemoryPreservesNetChange(t *testing.T) {
	p := New()
	s := Sample{
		UserFrames: frames("main"),
		Value:      10,
	}
	add(t, p, &s)
	s.Value = -20
	add(t, p, &s)
	if p.profile.Samples[0].Value != -10 {
		t.Fatal("signed net change lost")
	}
}

func TestAggregatorPreservesStructuredFrameBoundaries(t *testing.T) {
	for _, pair := range [][2]Sample{
		{{UserFrames: frames("foo;bar")}, {UserFrames: frames("foo", "bar")}},
		{{UserFrames: frames("same")}, {KernelFrames: frames("same")}},
	} {
		p := New()
		add(t, p, &pair[0])
		add(t, p, &pair[1])
		if len(p.profile.Samples) != 2 {
			t.Fatal(p.profile)
		}
	}
}

func TestAggregatorDrainClearsStackTraceState(t *testing.T) {
	p := New()
	add(t, p, &Sample{
		UserFrames: frames("main"),
		Value:      1,
	})
	p.Drain()
	if len(p.sampleIndexByKey) != 0 || len(p.stackBuckets) != 0 || len(p.profile.Frames) != 0 {
		t.Fatal("reset retained aggregation")
	}
}

func TestAggregatorDrainPreservesPreviousResult(t *testing.T) {
	p := New()
	s := Sample{
		UserFrames: frames("root", "leaf"),
		Value:      7,
	}
	add(t, p, &s)
	first := p.Drain()
	s.Value = 9
	add(t, p, &s)
	changedFirst := first.Samples[0].Value != 7
	incorrectCurrent := p.profile.Samples[0].Value != 9
	changedFrames := !slices.Equal(first.Frames, frames("root", "leaf"))
	if changedFirst || incorrectCurrent || changedFrames {
		t.Fatal("reset reused retained storage")
	}
}

func TestAggregatorSeparatesOffCPUCategories(t *testing.T) {
	p := New()
	for _, category := range []string{"blocked", "runqueue"} {
		add(t, p, &Sample{
			UserFrames: frames("main", "wait"),
			Labels:     []types.ProfileLabel{types.ProfileStringLabel("offcpu_category", category)},
			Value:      10,
		})
	}

	if len(p.profile.Samples) != 2 {
		t.Fatal("categories merged")
	}
}

func TestAggregatorAddRejectsWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		initial  int64
		rejected *Sample
	}{
		{
			name:    "nil",
			initial: 3,
		},
		{
			name:    "positive overflow",
			initial: math.MaxInt64,
			rejected: &Sample{
				UserFrames: frames("root"),
				Value:      1,
			},
		},
		{
			name:    "negative overflow",
			initial: math.MinInt64,
			rejected: &Sample{
				UserFrames: frames("root"),
				Value:      -1,
			},
		},
		{
			name:    "invalid labels with new stacks",
			initial: 3,
			rejected: &Sample{
				UserFrames:   frames("new-user"),
				KernelFrames: frames("new-kernel"),
				Labels:       []types.ProfileLabel{{Name: "invalid"}},
				Value:        1,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New()
			add(t, a, &Sample{
				UserFrames: frames("root"),
				Value:      tc.initial,
			})
			if err := a.Add(tc.rejected); err == nil {
				t.Fatal("accepted invalid sample")
			}

			// A subsequent lookup must still find the original sample at index zero.
			add(t, a, &Sample{UserFrames: frames("root")})
			got := a.Drain()
			want := types.AggregatedProfile{
				Frames: frames("root"),
				Stacks: [][]uint32{{0}},
				Samples: []types.AggregatedSample{{
					UserStack: 1,
					Value:     tc.initial,
				}},
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("rejection changed aggregation (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAggregatorOwnsInputAndDrainedStorage(t *testing.T) {
	a := New()
	sample := Sample{
		UserFrames:   frames("root", "user"),
		KernelFrames: frames("kernel"),
		Labels:       []types.ProfileLabel{types.ProfileStringLabel("comm", "first")},
		Value:        7,
	}
	add(t, a, &sample)
	sample.UserFrames[0].Name = "changed-root"
	sample.KernelFrames[0].Name = "changed-kernel"
	sample.Labels[0].StringValue = "changed-label"
	first := a.Drain()
	want := types.AggregatedProfile{
		Frames:    frames("root", "user", "kernel"),
		Stacks:    [][]uint32{{0, 1}, {2}},
		LabelSets: [][]types.ProfileLabel{{types.ProfileStringLabel("comm", "first")}},
		Samples: []types.AggregatedSample{{
			UserStack:   1,
			KernelStack: 2,
			LabelSet:    1,
			Value:       7,
		}},
	}
	if diff := cmp.Diff(want, first); diff != "" {
		t.Fatalf("retained mutable input (-want +got):\n%s", diff)
	}

	if !a.Empty() {
		t.Fatal("Drain retained samples")
	}

	sample.Value = 9
	add(t, a, &sample)
	second := a.Drain()
	if diff := cmp.Diff(want, first); diff != "" {
		t.Fatalf("new window changed drained storage (-want +got):\n%s", diff)
	}

	if len(second.Frames) != 3 || len(second.Stacks) != 2 || len(second.LabelSets) != 1 ||
		len(second.Samples) != 1 || second.Samples[0].UserStack != 1 ||
		second.Samples[0].KernelStack != 2 || second.Samples[0].LabelSet != 1 || second.Samples[0].Value != 9 {
		t.Fatalf("new window retained old identities: %+v", second)
	}

	// The caller owns the drained dictionaries, including nested slices.
	first.Frames[0].Name = "caller"
	first.Stacks[0][0] = 99
	first.LabelSets[0][0].StringValue = "caller"
	if second.Frames[0].Name != "changed-root" || second.Stacks[0][0] != 0 ||
		second.LabelSets[0][0].StringValue != "changed-label" {
		t.Fatal("drained windows share mutable dictionaries")
	}
}

func TestAggregatorEmptyAndZeroSamples(t *testing.T) {
	a := New()
	if !a.Empty() {
		t.Fatal("new aggregator is not empty")
	}

	if diff := cmp.Diff(types.AggregatedProfile{}, a.Drain()); diff != "" {
		t.Fatalf("empty Drain (-want +got):\n%s", diff)
	}

	add(t, a, &Sample{Value: 3})
	add(t, a, &Sample{Value: -3})
	if a.Empty() {
		t.Fatal("zero-valued sample treated as empty")
	}

	want := types.AggregatedProfile{Samples: []types.AggregatedSample{{}}}
	if diff := cmp.Diff(want, a.Drain()); diff != "" {
		t.Fatalf("empty stacks and labels (-want +got):\n%s", diff)
	}
}

func TestAggregatorSharesUserAndKernelStack(t *testing.T) {
	a := New()
	add(t, a, &Sample{
		UserFrames:   frames("shared"),
		KernelFrames: frames("shared"),
		Value:        1,
	})
	got := a.Drain()
	if len(got.Stacks) != 1 || len(got.Frames) != 1 ||
		got.Samples[0].UserStack != 1 || got.Samples[0].KernelStack != 1 {
		t.Fatalf("duplicate stack IDs: %+v", got)
	}
}

func TestAggregatorPreservesFrameAndLabelFields(t *testing.T) {
	for _, field := range []string{"frame name", "frame file", "frame line", "label name", "label kind", "label value", "label unit"} {
		t.Run(field, func(t *testing.T) {
			a := New()
			first := Sample{
				UserFrames: []types.ProfileFrame{{
					Name: "work",
					File: "source.go",
					Line: 10,
				}},
				Labels: []types.ProfileLabel{{
					Name:        "weight",
					Kind:        types.ProfileLabelNumber,
					NumberValue: 1,
					NumberUnit:  "bytes",
				}},
				Value: 3,
			}
			second := first
			second.UserFrames = slices.Clone(first.UserFrames)
			second.Labels = slices.Clone(first.Labels)
			switch field {
			case "frame name":
				second.UserFrames[0].Name = "other"
			case "frame file":
				second.UserFrames[0].File = "other.go"
			case "frame line":
				second.UserFrames[0].Line++
			case "label name":
				second.Labels[0].Name = "other"
			case "label kind":
				second.Labels[0] = types.ProfileStringLabel("weight", "1")
			case "label value":
				second.Labels[0].NumberValue++
			case "label unit":
				second.Labels[0].NumberUnit = "count"
			}

			add(t, a, &first)
			add(t, a, &second)
			add(t, a, &first)
			got := a.Drain()
			if len(got.Samples) != 2 || got.Samples[0].Value != 6 || got.Samples[1].Value != 3 {
				t.Fatalf("distinct identities merged: %+v", got)
			}
		})
	}
}

func TestAggregatorDrainBoundsIndexRetention(t *testing.T) {
	for _, count := range []int{256, maxRetainedIndexEntries + 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			a := New()
			for i := range count {
				text := strconv.Itoa(i)
				add(t, a, &Sample{
					UserFrames: frames(text),
					Labels:     []types.ProfileLabel{types.ProfileStringLabel("key", text)},
					Value:      1,
				})
			}
			first := a.Drain()
			if len(a.frameIDs) != 0 || len(a.stackBuckets) != 0 || len(a.labelBuckets) != 0 || len(a.sampleIndexByKey) != 0 ||
				!a.Empty() {
				t.Fatal("Drain retained dictionary entries")
			}

			if count > maxRetainedIndexEntries {
				if a.frameIDs != nil || a.stackBuckets != nil || a.labelBuckets != nil || a.sampleIndexByKey != nil {
					t.Fatal("Drain retained oversized indexes")
				}
			} else if a.frameIDs == nil || a.stackBuckets == nil || a.labelBuckets == nil || a.sampleIndexByKey == nil {
				t.Fatal("Drain discarded bounded indexes")
			}

			add(t, a, &Sample{
				UserFrames: frames("next"),
				Labels:     []types.ProfileLabel{types.ProfileStringLabel("key", "next")},
				Value:      2,
			})
			second := a.Drain()
			if len(second.Samples) != 1 || second.Samples[0].UserStack != 1 || second.Samples[0].LabelSet != 1 ||
				second.Samples[0].Value != 2 || len(first.Samples) != count || first.Samples[0].Value != 1 {
				t.Fatal("reused indexes corrupted window identity")
			}
		})
	}
}

func TestAggregatorAddStackCombinations(t *testing.T) {
	for _, mode := range []string{"empty", "kernel only", "user only", "same new", "distinct new", "known user", "known kernel", "both known"} {
		t.Run(mode, func(t *testing.T) {
			a := New()
			sample := Sample{Value: 3}
			wantStacks := 0
			switch mode {
			case "kernel only":
				sample.KernelFrames = frames("kernel")
				wantStacks = 1
			case "user only":
				sample.UserFrames = frames("user")
				wantStacks = 1
			case "same new":
				sample.UserFrames = frames("shared", "leaf")
				sample.KernelFrames = frames("shared", "leaf")
				wantStacks = 1
			case "distinct new", "known user", "known kernel", "both known":
				sample.UserFrames = frames("shared", "user")
				sample.KernelFrames = frames("shared", "kernel")
				wantStacks = 2
			}

			if mode == "known user" || mode == "both known" {
				add(t, a, &Sample{UserFrames: sample.UserFrames})
			}

			if mode == "known kernel" || mode == "both known" {
				add(t, a, &Sample{KernelFrames: sample.KernelFrames})
			}

			add(t, a, &sample)
			add(t, a, &sample)
			result := a.Drain()
			if len(result.Stacks) != wantStacks {
				t.Fatalf("stack count = %d, want %d", len(result.Stacks), wantStacks)
			}

			got := result.Samples[len(result.Samples)-1]
			if got.Value != 6 {
				t.Fatalf("value = %d, want 6", got.Value)
			}

			resolve := func(id uint32) []types.ProfileFrame {
				if id == 0 {
					return nil
				}

				var resolved []types.ProfileFrame
				for _, frameID := range result.Stacks[id-1] {
					resolved = append(resolved, result.Frames[frameID])
				}

				return resolved
			}

			if diff := cmp.Diff(sample.UserFrames, resolve(got.UserStack)); diff != "" {
				t.Fatalf("user stack mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(sample.KernelFrames, resolve(got.KernelStack)); diff != "" {
				t.Fatalf("kernel stack mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAggregatorCopyProfileOwnsAllSlices(t *testing.T) {
	a := New()
	sample := Sample{
		UserFrames:   frames("allocate"),
		KernelFrames: frames("fault"),
		Labels:       []types.ProfileLabel{types.ProfilePIDLabel(42)},
		Value:        -4096,
	}

	add(t, a, &sample)
	first := a.CopyProfile()
	changed := a.CopyProfile()
	changed.Frames[0].Name = "changed"
	changed.Stacks[0][0] = 1
	changed.LabelSets[0][0].NumberValue = 99
	changed.Samples[0].Value = 0
	if diff := cmp.Diff(first, a.CopyProfile()); diff != "" {
		t.Fatalf("copy aliases retained state (-want +got):\n%s", diff)
	}

	sample.Value = 4096
	add(t, a, &sample)
	if a.Empty() || a.CopyProfile().Samples[0].Value != 0 || first.Samples[0].Value != -4096 {
		t.Fatal("copy reset state, discarded zero identity, or changed after Add")
	}

	drained := a.Drain()
	if !a.Empty() || len(drained.Samples) != 1 || first.Frames[0].Name != "allocate" {
		t.Fatal("Drain changed a prior copy")
	}
}
