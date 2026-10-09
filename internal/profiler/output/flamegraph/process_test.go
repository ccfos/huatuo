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

package flamegraph

import (
	"bytes"
	"math"
	"strconv"
	"strings"
	"testing"
)

// buildProcessor folds stacks into a finalized processor.
func buildProcessor(stacks ...Stack) *processor {
	var proc processor

	for i := range stacks {
		proc.Process(stacks[i])
	}
	proc.Finalize()

	return &proc
}

// lookup walks the tree and returns the frame at the given name path.
func lookup(t *testing.T, frames []frame, path ...string) frame {
	t.Helper()

	if len(path) == 0 {
		t.Fatal("lookup needs at least a frame name")
	}

	for i := range frames {
		if frames[i].Name != path[0] {
			continue
		}

		if len(path) == 1 {
			return frames[i]
		}

		return lookup(t, frames[i].Children, path[1:]...)
	}

	t.Fatalf("frame path %v not found", path)

	return frame{}
}

func assertPercent(t *testing.T, label string, got, want float32) {
	t.Helper()

	if math.Abs(float64(got-want)) > 1e-4 {
		t.Errorf("%s: SamplePercent = %.4f, want %.4f", label, got, want)
	}
}

// TestPercentIncludesParentSelfSamples covers the reported case: a parent with
// its own samples must not hand its full width to its children.
func TestPercentIncludesParentSelfSamples(t *testing.T) {
	proc := buildProcessor(
		Stack{Names: []string{"root"}, Samples: 75},
		Stack{Names: []string{"root", "child"}, Samples: 25},
	)

	root := lookup(t, proc.frames, "root")
	child := lookup(t, root.Children, "child")

	assertPercent(t, "root", root.SamplePercent, 100)
	// The parent holds 75 self samples, so the child may only span 25.
	assertPercent(t, "child", child.SamplePercent, 25)
	assertPercent(t, "child.LeftPercent", child.LeftPercent, root.LeftPercent)
}

// TestPercentAcrossMultipleRoots checks that a shared total keeps sibling
// roots proportional to the whole profile.
func TestPercentAcrossMultipleRoots(t *testing.T) {
	proc := buildProcessor(
		Stack{Names: []string{"a"}, Samples: 50},
		Stack{Names: []string{"b"}, Samples: 30},
	)

	a := lookup(t, proc.frames, "a")
	b := lookup(t, proc.frames, "b")

	assertPercent(t, "a", a.SamplePercent, 62.5)
	assertPercent(t, "b", b.SamplePercent, 37.5)
	assertPercent(t, "a.LeftPercent", a.LeftPercent, 0)
	assertPercent(t, "b.LeftPercent", b.LeftPercent, 62.5)
}

// TestPercentWithNestedSelfSamples checks that unoccupied space under a parent
// equals that parent's own samples, at every depth.
func TestPercentWithNestedSelfSamples(t *testing.T) {
	proc := buildProcessor(
		Stack{Names: []string{"root"}, Samples: 50},
		Stack{Names: []string{"root", "child"}, Samples: 30},
		Stack{Names: []string{"root", "child", "grand"}, Samples: 20},
	)

	root := lookup(t, proc.frames, "root")
	child := lookup(t, root.Children, "child")
	grand := lookup(t, child.Children, "grand")

	// Frame counts are inclusive, so the profile holds 50+30+20 = 100 samples
	// and root accounts for all of them while child accounts for 30+20.
	assertPercent(t, "root", root.SamplePercent, 100)
	assertPercent(t, "child", child.SamplePercent, 50)
	assertPercent(t, "grand", grand.SamplePercent, 20)
	assertPercent(t, "child.LeftPercent", child.LeftPercent, 0)
	assertPercent(t, "grand.LeftPercent", grand.LeftPercent, 0)
}

// TestChildrenNeverExceedParent asserts the invariant the renderer depends on:
// children plus the parent's own samples exactly fill the parent.
func TestChildrenNeverExceedParent(t *testing.T) {
	proc := buildProcessor(
		Stack{Names: []string{"root"}, Samples: 50},
		Stack{Names: []string{"root", "a"}, Samples: 20},
		Stack{Names: []string{"root", "b"}, Samples: 10},
		Stack{Names: []string{"root", "b", "deep"}, Samples: 5},
		Stack{Names: []string{"other"}, Samples: 15},
	)

	var walk func(frames []frame)

	walk = func(frames []frame) {
		for i := range frames {
			childPct := float32(0)
			childSamples := int64(0)

			for j := range frames[i].Children {
				childPct += frames[i].Children[j].SamplePercent
				childSamples += frames[i].Children[j].SampleCount
			}

			if childPct > frames[i].SamplePercent+1e-4 {
				t.Errorf("frame %q: children span %.4f%% but the frame is only %.4f%% wide",
					frames[i].Name, childPct, frames[i].SamplePercent)
			}

			// A frame whose own samples sit between it and its children must
			// leave that space unoccupied, so the children cannot fill it.
			if self := frames[i].SampleCount - childSamples; self > 0 {
				if childPct >= frames[i].SamplePercent-1e-4 {
					t.Errorf("frame %q: %d self samples but children still span %.4f%% of %.4f%%",
						frames[i].Name, self, childPct, frames[i].SamplePercent)
				}
			}

			if frames[i].LeftPercent+frames[i].SamplePercent > 100.0001 {
				t.Errorf("frame %q extends past the graph: left %.4f + width %.4f",
					frames[i].Name, frames[i].LeftPercent, frames[i].SamplePercent)
			}

			walk(frames[i].Children)
		}
	}

	walk(proc.frames)
}

// TestRenderedWidthsFollowSamplePercent pins the reported symptom at the SVG
// level: the tooltip used to read "25 samples, 100.00%" for a frame that only
// owns a quarter of the profile.
func TestRenderedWidthsFollowSamplePercent(t *testing.T) {
	var output bytes.Buffer

	stacks := []Stack{
		{Names: []string{"root"}, Samples: 75},
		{Names: []string{"root", "child"}, Samples: 25},
	}

	if err := RenderStyle(stacks, &output, DefaultStyle); err != nil {
		t.Fatalf("RenderStyle() error = %v", err)
	}

	svg := output.String()

	if !strings.Contains(svg, `data-title="child (25 samples, 25.00%)"`) {
		t.Errorf("child tooltip does not report its true share:\n%s", svg)
	}

	usableWidth := float32(DefaultStyle.ImageWidth) - DefaultStyle.XMargin*2
	wantWidth := usableWidth * 0.25
	// The renderer prints rect widths with "%.1f".
	wantWidthText := strconv.FormatFloat(float64(wantWidth), 'f', 1, 32)

	if !strings.Contains(svg, wantWidthText) {
		t.Errorf("child rect width is not %s (a quarter of %.1f):\n%s",
			wantWidthText, usableWidth, svg)
	}
}
