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
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// FramesToFolded converts depth-first flame graph frames to folded stacks.
func FramesToFolded(frames []FrameData) ([]byte, error) {
	counts := make(map[string]int64, len(frames))
	stack := make([]string, 0, 32)

	for i, frame := range frames {
		if err := validateFoldedFrame(i, frame, len(stack)); err != nil {
			return nil, err
		}

		stack = stack[:int(frame.Level)]
		stack = append(stack, sanitizeFoldedLabel(frame.Label))
		if frame.Self == 0 {
			continue
		}

		path := strings.Join(stack, ";")
		if counts[path] > math.MaxInt64-frame.Self {
			return nil, fmt.Errorf(
				"frame %d: folded count overflows for stack %q",
				i,
				path,
			)
		}
		counts[path] += frame.Self
	}

	paths := make([]string, 0, len(counts))
	for path := range counts {
		paths = append(paths, path)
	}
	// Stable output makes exported snapshots reproducible and diffable.
	sort.Strings(paths)

	var output bytes.Buffer
	for _, path := range paths {
		output.Grow(len(path) + 22)
		output.WriteString(path)
		output.WriteByte(' ')
		output.Write(strconv.AppendInt(nil, counts[path], 10))
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func validateFoldedFrame(index int, frame FrameData, stackDepth int) error {
	if frame.Level < 0 || frame.Level > int64(stackDepth) {
		return fmt.Errorf(
			"frame %d: invalid level %d for stack depth %d",
			index,
			frame.Level,
			stackDepth,
		)
	}
	if frame.Value < 0 {
		return fmt.Errorf("frame %d: aggregate value must not be negative", index)
	}
	if frame.Self < 0 {
		return fmt.Errorf("frame %d: self value must not be negative", index)
	}
	if frame.Self > frame.Value {
		return fmt.Errorf(
			"frame %d: self value %d exceeds aggregate value %d",
			index,
			frame.Self,
			frame.Value,
		)
	}
	return nil
}

func sanitizeFoldedLabel(label string) string {
	label = strings.Map(func(current rune) rune {
		switch {
		case current == ';':
			return '_'
		case unicode.IsControl(current):
			return ' '
		default:
			return current
		}
	}, label)
	label = strings.TrimSpace(label)
	if label == "" {
		return "(unknown)"
	}
	return label
}
