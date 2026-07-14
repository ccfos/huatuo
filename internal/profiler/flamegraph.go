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

package profiler

import (
	"fmt"
	"time"

	"huatuo-bamai/internal/flamegraph"
)

// ParseFlamegraphFrames converts Grafana nested-set flame graph frames to a
// pprof profile. Self values become leaf samples and Value is retained only as
// the aggregate value supplied by the nested-set representation.
func ParseFlamegraphFrames(
	startTime time.Time,
	profileType string,
	frames []flamegraph.FrameData,
	opt *ParseOption,
) (*ProfileData, error) {
	stack := make([][]byte, 0, 32)
	tree := make([]*TreeItem, 0, len(frames))

	for i, frame := range frames {
		if frame.Level < 0 || frame.Level > int64(len(stack)) {
			return nil, fmt.Errorf("frame %d: invalid level %d, stack depth %d", i, frame.Level, len(stack))
		}

		stack = stack[:int(frame.Level)]
		label := frame.Label
		if label == "" {
			label = "(unknown)"
		}
		stack = append(stack, []byte(label))
		if frame.Self <= 0 {
			continue
		}

		itemStack := make([][]byte, len(stack))
		for j := range stack {
			itemStack[j] = append([]byte(nil), stack[j]...)
		}
		tree = append(tree, &TreeItem{Stack: itemStack, Value: uint64(frame.Self)})
	}

	if len(tree) == 0 {
		return nil, fmt.Errorf("flame graph has no positive self samples")
	}
	return ParseTree(startTime, profileType, tree, opt)
}
