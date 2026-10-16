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
	"strings"
	"testing"
)

func TestFramePercentagesPreserveSelfSamples(t *testing.T) {
	var p processor
	for _, stack := range []Stack{
		{Names: []string{"a"}, Samples: 40},
		{Names: []string{"a", "child"}, Samples: 20},
		{Names: []string{"a", "child", "leaf"}, Samples: 10},
		{Names: []string{"a", "sibling"}, Samples: 10},
		{Names: []string{"b"}, Samples: 20},
	} {
		p.Process(stack)
	}
	p.Finalize()
	roots, _ := p.Result()
	check := func(f frame, samples int64, pct, left float32) {
		t.Helper()
		if f.SampleCount != samples || f.SamplePercent != pct || f.LeftPercent != left {
			t.Errorf("%s: got count=%d percent=%g left=%g; want %d, %g, %g",
				f.Name, f.SampleCount, f.SamplePercent, f.LeftPercent, samples, pct, left)
		}
	}
	check(roots[0], 80, 80, 0)
	check(roots[0].Children[0], 30, 30, 0)
	check(roots[0].Children[0].Children[0], 10, 10, 0)
	check(roots[0].Children[1], 10, 10, 30)
	check(roots[1], 20, 20, 80)
}

func TestRenderSelfSamples(t *testing.T) {
	var svg bytes.Buffer
	err := RenderStyle([]Stack{
		{Names: []string{"root"}, Samples: 75},
		{Names: []string{"root", "child"}, Samples: 25},
	}, &svg, DefaultStyle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(svg.String(), "child (25 samples, 25.00%)") {
		t.Fatal("child tooltip must report 25% of all samples")
	}
	if !strings.Contains(svg.String(), `width="295.0"`) {
		t.Fatal("child width must be one quarter of the 1180-pixel plot")
	}
}

func TestFramePercentagesEmpty(t *testing.T) {
	var p processor
	p.Process(Stack{Names: []string{"root", "child"}})
	p.Finalize()
	roots, _ := p.Result()
	if roots[0].SamplePercent != 0 || roots[0].Children[0].SamplePercent != 0 {
		t.Fatal("zero samples must retain zero percentages")
	}
}
