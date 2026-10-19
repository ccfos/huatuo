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

import "sort"

// Stack represents a single stack trace with a sample count.
type Stack struct {
	// Names holds function names from outermost (index 0) to innermost.
	Names []string
	// Samples is the number of times this stack appeared.
	Samples int64
}

type processor struct {
	frames   []frame
	maxDepth int
	// childIndexes holds one name→position map per parent frame (root is
	// keyed by rootIndex), replacing the linear findFrame scan per
	// processed stack frame.
	childIndexes map[int]map[string]int
}

const rootIndex = -1

func (p *processor) Process(stack Stack) {
	frames := &p.frames

	n := len(stack.Names)
	if n > p.maxDepth {
		p.maxDepth = n
	}

	parent := rootIndex
	for depth, name := range stack.Names {
		if p.childIndexes == nil {
			p.childIndexes = make(map[int]map[string]int)
		}
		level := p.childIndexes[parent]

		index, ok := level[name]
		if !ok {
			f := frame{
				Name:  name,
				Depth: depth,
			}
			*frames = append(*frames, f)
			index = len(*frames) - 1
			if level == nil {
				level = make(map[string]int)
				p.childIndexes[parent] = level
			}
			level[name] = index
		}
		fr := &(*frames)[index]
		fr.SampleCount += stack.Samples
		frames = &fr.Children
		parent = index
	}
}

func (p *processor) Finalize() {
	p.sort(p.frames)
	p.calcPcts(p.frames, 100, 0)
}

func (p *processor) Result() (frames []frame, maxDepth int) {
	return p.frames, p.maxDepth
}

func (p *processor) sort(frames []frame) {
	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Name < frames[j].Name
	})

	for i := range frames {
		p.sort(frames[i].Children)
	}
}

func (p *processor) calcPcts(frames []frame, totalPct, leftPct float32) {
	var total int64

	for i := range frames {
		total += frames[i].SampleCount
	}

	if total == 0 {
		return
	}

	d := leftPct

	for i := range frames {
		pct := float32(frames[i].SampleCount) / float32(total) * totalPct
		frames[i].SamplePercent = pct
		frames[i].LeftPercent = d
		d += pct
	}

	for i := range frames {
		p.calcPcts(frames[i].Children, frames[i].SamplePercent, frames[i].LeftPercent)
	}
}
