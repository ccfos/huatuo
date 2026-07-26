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
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestFramesToFolded(t *testing.T) {
	tests := []struct {
		name   string
		frames []FrameData
		want   string
	}{
		{
			name: "nested branches",
			frames: []FrameData{
				{Level: 0, Value: 15, Self: 1, Label: "root"},
				{Level: 1, Value: 10, Self: 0, Label: "worker"},
				{Level: 2, Value: 6, Self: 6, Label: "read"},
				{Level: 2, Value: 4, Self: 4, Label: "write"},
				{Level: 1, Value: 4, Self: 4, Label: "idle"},
			},
			want: "root 1\n" +
				"root;idle 4\n" +
				"root;worker;read 6\n" +
				"root;worker;write 4\n",
		},
		{
			name: "multiple roots",
			frames: []FrameData{
				{Level: 0, Value: 2, Self: 1, Label: "pid-b"},
				{Level: 1, Value: 1, Self: 1, Label: "work-b"},
				{Level: 0, Value: 3, Self: 1, Label: "pid-a"},
				{Level: 1, Value: 2, Self: 2, Label: "work-a"},
			},
			want: "pid-a 1\npid-a;work-a 2\npid-b 1\npid-b;work-b 1\n",
		},
		{
			name: "repeated paths",
			frames: []FrameData{
				{Level: 0, Value: 2, Self: 2, Label: "same"},
				{Level: 0, Value: 3, Self: 3, Label: "same"},
			},
			want: "same 5\n",
		},
		{
			name: "sanitized labels",
			frames: []FrameData{
				{Level: 0, Value: 1, Self: 1, Label: "main;work"},
				{Level: 0, Value: 1, Self: 1, Label: "line\nbreak"},
				{Level: 0, Value: 1, Self: 1, Label: "\t"},
			},
			want: "(unknown) 1\nline break 1\nmain_work 1\n",
		},
		{
			name:   "empty profile",
			frames: nil,
			want:   "",
		},
		{
			name: "zero self",
			frames: []FrameData{
				{Level: 0, Value: 1, Label: "root"},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FramesToFolded(tt.frames)
			if err != nil {
				t.Fatalf("FramesToFolded() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("FramesToFolded() = %q, want %q", got, tt.want)
			}
			if gotSum := foldedSum(t, got); gotSum != positiveSelfSum(tt.frames) {
				t.Fatalf(
					"folded count sum = %d, want %d",
					gotSum,
					positiveSelfSum(tt.frames),
				)
			}
		})
	}
}

func TestFramesToFoldedRejectsInvalidFrames(t *testing.T) {
	tests := []struct {
		name   string
		frames []FrameData
		want   string
	}{
		{
			name:   "negative level",
			frames: []FrameData{{Level: -1}},
			want:   "invalid level",
		},
		{
			name: "level jump",
			frames: []FrameData{
				{Level: 0, Value: 1},
				{Level: 2, Value: 1},
			},
			want: "invalid level",
		},
		{
			name:   "negative aggregate",
			frames: []FrameData{{Level: 0, Value: -1}},
			want:   "aggregate value must not be negative",
		},
		{
			name:   "negative self",
			frames: []FrameData{{Level: 0, Value: 1, Self: -1}},
			want:   "self value must not be negative",
		},
		{
			name:   "self exceeds aggregate",
			frames: []FrameData{{Level: 0, Value: 1, Self: 2}},
			want:   "self value 2 exceeds aggregate value 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FramesToFolded(tt.frames)
			if err == nil {
				t.Fatal("FramesToFolded() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("FramesToFolded() error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestFramesToFoldedRejectsCountOverflow(t *testing.T) {
	frames := []FrameData{
		{Level: 0, Value: math.MaxInt64, Self: math.MaxInt64, Label: "root"},
		{Level: 0, Value: 1, Self: 1, Label: "root"},
	}

	_, err := FramesToFolded(frames)
	if err == nil {
		t.Fatal("FramesToFolded() error = nil, want overflow error")
	}
	if !strings.Contains(err.Error(), "folded count overflows") {
		t.Fatalf("FramesToFolded() error = %q, want overflow error", err)
	}
}

func TestFramesToFoldedSupportsMaximumCount(t *testing.T) {
	frames := []FrameData{{
		Level: 0,
		Value: math.MaxInt64,
		Self:  math.MaxInt64,
		Label: "root",
	}}
	want := fmt.Sprintf("root %d\n", int64(math.MaxInt64))

	got, err := FramesToFolded(frames)
	if err != nil {
		t.Fatalf("FramesToFolded() error = %v", err)
	}
	if string(got) != want {
		t.Fatalf("FramesToFolded() = %q, want %q", got, want)
	}
}

func foldedSum(t *testing.T, folded []byte) int64 {
	t.Helper()

	var sum int64
	for _, line := range strings.Split(strings.TrimSpace(string(folded)), "\n") {
		if line == "" {
			continue
		}

		separator := strings.LastIndexByte(line, ' ')
		if separator <= 0 || separator == len(line)-1 {
			t.Fatalf("invalid folded line %q", line)
		}
		count, err := strconv.ParseInt(line[separator+1:], 10, 64)
		if err != nil {
			t.Fatalf("invalid folded count in %q: %v", line, err)
		}
		if sum > math.MaxInt64-count {
			t.Fatalf("folded count sum overflows for %q", line)
		}
		sum += count
	}
	return sum
}

func positiveSelfSum(frames []FrameData) int64 {
	var sum int64
	for _, frame := range frames {
		if frame.Self > 0 {
			sum += frame.Self
		}
	}
	return sum
}
