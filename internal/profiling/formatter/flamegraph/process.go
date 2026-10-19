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
	"sort"
	"strings"
)

// Stack represents a single stack trace with an aggregate sample weight.
type Stack struct {
	// Names holds function names from outermost (index 0) to innermost.
	Names []string
	// Samples holds the aggregate weight in the profile's selected unit.
	Samples int64
}

var foldedReplacer = strings.NewReplacer(
	";",
	":",
	"\r",
	" ",
	"\n",
	" ",
)

type processor struct {
	frames   []frame
	index    map[string]int
	maxDepth int
}

func (p *processor) Process(stack Stack) {
	frames := &p.frames
	index := &p.index

	depth := 0
	for _, name := range stack.Names {
		// Preserve folded output normalization without encoding and splitting stacks.
		name = strings.TrimSpace(foldedReplacer.Replace(name))
		if name == "" {
			continue
		}

		// Small fan-outs avoid a map allocation; wide trees use indexed lookup.
		if *index == nil && len(*frames) > 8 {
			*index = make(map[string]int, len(*frames))
			for i := range *frames {
				(*index)[(*frames)[i].Name] = i
			}
		}

		var fr *frame
		if *index != nil {
			if i, ok := (*index)[name]; ok {
				fr = &(*frames)[i]
			}
		} else {
			fr = findFrame(*frames, name)
		}

		if fr == nil {
			f := frame{
				Name:  name,
				Depth: depth,
			}
			*frames = append(*frames, f)
			if *index != nil {
				(*index)[name] = len(*frames) - 1
			}

			fr = &(*frames)[len(*frames)-1]
		}

		fr.SampleCount += stack.Samples
		frames = &fr.Children
		index = &fr.childIndex
		depth++
	}

	if depth > p.maxDepth {
		p.maxDepth = depth
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

func findFrame(frames []frame, name string) *frame {
	for i := range frames {
		if frames[i].Name == name {
			return &frames[i]
		}
	}

	return nil
}

func (p *processor) prune() { p.frames, p.maxDepth = pruneFrames(p.frames) }

func pruneFrames(frames []frame) ([]frame, int) {
	result := frames[:0]
	maxDepth := 0
	for i := range frames {
		fr := &frames[i]
		var childDepth int
		fr.Children, childDepth = pruneFrames(fr.Children)
		if fr.SampleCount != 0 || len(fr.Children) != 0 {
			result = append(result, *fr)
			maxDepth = max(maxDepth, fr.Depth+1, childDepth)
		}
	}

	clear(frames[len(result):])

	return result, maxDepth
}
