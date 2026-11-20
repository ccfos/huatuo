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
	"encoding/xml"
	"math"
	"strings"
	"testing"
)

func TestRenderKeepsSymbolsOutOfJavaScript(t *testing.T) {
	symbol := `');alert(1);//<script>alert(2)</script>`
	var proc processor
	proc.Process(Stack{
		Names:   []string{"root", symbol},
		Samples: 1,
	})

	var output bytes.Buffer
	if err := proc.render(&output, DefaultStyle); err != nil {
		t.Fatalf("render flame graph: %v", err)
	}

	content := output.String()
	if strings.Contains(content, `onmouseover="s('`) ||
		strings.Contains(content, "<script>alert(2)</script>") {
		t.Fatalf("SVG embeds an untrusted symbol as executable markup: %s", content)
	}

	if !strings.Contains(content, `data-title="`) ||
		!strings.Contains(content, "&lt;script&gt;alert(2)&lt;/script&gt;") {
		t.Fatalf("SVG does not preserve the escaped symbol as data: %s", content)
	}
}

func TestFramePercentagesPreserveSelfSamples(t *testing.T) {
	var p processor
	for _, stack := range []Stack{
		{
			Names:   []string{"a"},
			Samples: 40,
		},
		{
			Names:   []string{"a", "child"},
			Samples: 20,
		},
		{
			Names:   []string{"a", "child", "leaf"},
			Samples: 10,
		},
		{
			Names:   []string{"a", "sibling"},
			Samples: 10,
		},
		{
			Names:   []string{"b"},
			Samples: 20,
		},
	} {
		p.Process(stack)
	}
	p.Finalize()
	roots, _ := p.Result()
	check := func(f frame, samples int64, pct, left float32) {
		t.Helper()
		// float32 percentages need not be exact across equivalent operation orders.
		if f.SampleCount != samples || !(math.Abs(float64(f.SamplePercent-pct)) <= 0.0001) ||
			!(math.Abs(float64(f.LeftPercent-left)) <= 0.0001) {
			t.Errorf("%s: got count=%d percent=%g left=%g; want %d, %g, %g",
				f.Name, f.SampleCount, f.SamplePercent, f.LeftPercent, samples, pct, left)
		}
	}
	if len(roots) != 2 || len(roots[0].Children) != 2 || len(roots[0].Children[0].Children) != 1 {
		t.Fatalf("unexpected frame tree: %+v", roots)
	}
	check(roots[0], 80, 80, 0)
	check(roots[0].Children[0], 30, 30, 0)
	check(roots[0].Children[0].Children[0], 10, 10, 0)
	check(roots[0].Children[1], 10, 10, 30)
	check(roots[1], 20, 20, 80)
}

func TestRenderSelfSamples(t *testing.T) {
	var svg bytes.Buffer
	var p processor
	for _, stack := range []Stack{
		{
			Names:   []string{"root"},
			Samples: 75,
		},
		{
			Names:   []string{"root", "child"},
			Samples: 25,
		},
	} {
		p.Process(stack)
	}
	err := p.render(&svg, DefaultStyle)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Frames []struct {
			Title string `xml:"title"`
			Rect  struct {
				X     float64 `xml:"x,attr"`
				Width float64 `xml:"width,attr"`
			} `xml:"rect"`
		} `xml:"g"`
	}
	if err := xml.Unmarshal(svg.Bytes(), &document); err != nil {
		t.Fatalf("decode rendered SVG: %v", err)
	}
	for _, f := range document.Frames {
		if f.Title != "child (25 samples, 25.00%)" {
			continue
		}
		if f.Rect.X != 10 || f.Rect.Width != 295 {
			t.Fatalf("child rectangle = x %g, width %g; want 10, 295", f.Rect.X, f.Rect.Width)
		}
		return
	}
	t.Fatal("rendered child must report 25 samples and 25% of all samples")
}

func TestFramePercentagesEmpty(t *testing.T) {
	var p processor
	p.Process(Stack{Names: []string{"root", "child"}})
	p.Finalize()
	roots, _ := p.Result()
	if len(roots) != 1 || len(roots[0].Children) != 1 {
		t.Fatalf("unexpected zero-sample frame tree: %+v", roots)
	}
	if roots[0].SamplePercent != 0 || roots[0].Children[0].SamplePercent != 0 {
		t.Fatal("zero samples must retain zero percentages")
	}
}
