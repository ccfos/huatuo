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

// Package collapsed writes folded-stack format (Brendan Gregg): "frame;frame count\n".
package collapsed

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ccfos/huatuo/internal/profiling/window"
)

// Stacks accumulates folded stack weights. Its zero value is ready to use.
// It retains encoded keys, so callers may reuse their frame slices after Add.
type Stacks struct {
	counts map[string]int64
}

func (s *Stacks) Add(frames []string, weight int64) error {
	if len(frames) == 0 {
		return nil
	}

	if s.counts == nil {
		s.counts = make(map[string]int64)
	}

	key := foldedStackKey(frames)
	value, err := window.AddValues(s.counts[key], weight)
	if err != nil {
		return err
	}

	s.counts[key] = value
	// Balanced allocations have no visual weight and must not create output.
	if s.counts[key] == 0 {
		delete(s.counts, key)
	}

	return nil
}

func foldedStackKey(frames []string) string {
	if len(frames) == 0 {
		return ""
	}

	length := len(frames) - 1
	for _, frame := range frames {
		length += len(frame)
	}

	var key strings.Builder
	key.Grow(length)
	for index, frame := range frames {
		if index > 0 {
			key.WriteByte(';')
		}

		writeFoldedFrame(&key, frame)
	}

	return key.String()
}

func writeFoldedFrame(key *strings.Builder, frame string) {
	start := 0
	for index := range len(frame) {
		var replacement byte
		switch frame[index] {
		case ';':
			replacement = ':'
		case '\r', '\n':
			replacement = ' '
		default:
			continue
		}

		key.WriteString(frame[start:index])
		key.WriteByte(replacement)
		start = index + 1
	}

	key.WriteString(frame[start:])
}

func (s *Stacks) Write(w io.Writer) error {
	keys := make([]string, 0, len(s.counts))

	for k := range s.counts {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		if _, err := fmt.Fprintf(
			w,
			"%s %d\n",
			k,
			s.counts[k],
		); err != nil {
			return err
		}
	}

	return nil
}

// IsEmpty reports whether all stack weights are zero.
func (s *Stacks) IsEmpty() bool {
	return len(s.counts) == 0
}
