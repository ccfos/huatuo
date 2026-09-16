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

package raw

import (
	"bytes"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

func TestFormatterAdd_RemovesBalancedStack(t *testing.T) {
	formatter := New()
	frames := []string{"root", "allocate"}

	if err := formatter.Add(&output.Sample{Frames: frames, Count: 4096}); err != nil {
		t.Fatalf("add allocation sample: %v", err)
	}
	if err := formatter.Add(&output.Sample{Frames: frames, Count: -4096}); err != nil {
		t.Fatalf("add free sample: %v", err)
	}

	if !formatter.IsEmpty() {
		t.Fatalf("balanced stack retained in formatter: %v", formatter.Counts())
	}

	var got bytes.Buffer
	if err := formatter.Write(&got); err != nil {
		t.Fatalf("write formatter: %v", err)
	}
	if got.Len() != 0 {
		t.Fatalf("balanced stack output = %q, want empty", got.String())
	}
}

func TestFormatterAddNormalizesFoldedFrames(t *testing.T) {
	tests := []struct {
		name   string
		frames []string
		want   string
	}{
		{
			name:   "plain frames remain unchanged",
			frames: []string{"root", "main.handle"},
			want:   "root;main.handle 3\n",
		},
		{
			name:   "delimiter and line breaks are normalized",
			frames: []string{"root", "generic::<[u8; 7]>", "line\r\nbreak"},
			want:   "root;generic::<[u8: 7]>;line  break 3\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			formatter := New()
			if err := formatter.Add(&output.Sample{Frames: tt.frames, Count: 3}); err != nil {
				t.Fatalf("add sample: %v", err)
			}

			var got bytes.Buffer
			if err := formatter.Write(&got); err != nil {
				t.Fatalf("write formatter: %v", err)
			}
			if got.String() != tt.want {
				t.Fatalf("output = %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestFormatterAddMergesNormalizationCollisions(t *testing.T) {
	formatter := New()
	samples := []*output.Sample{
		{Frames: []string{"root", "foo;bar"}, Count: 2},
		{Frames: []string{"root", "foo:bar"}, Count: 3},
	}
	for _, sample := range samples {
		if err := formatter.Add(sample); err != nil {
			t.Fatalf("add sample: %v", err)
		}
	}

	var got bytes.Buffer
	if err := formatter.Write(&got); err != nil {
		t.Fatalf("write formatter: %v", err)
	}
	const want = "root;foo:bar 5\n"
	if got.String() != want {
		t.Fatalf("output = %q, want %q", got.String(), want)
	}
}

func BenchmarkFormatterAdd(b *testing.B) {
	tests := []struct {
		name   string
		frames []string
	}{
		{
			name: "plain frames",
			frames: []string{
				"process 123:worker",
				"runtime.goexit",
				"net/http.(*conn).serve",
			},
		},
		{
			name: "frames requiring normalization",
			frames: []string{
				"process 123:worker",
				"generic::<[u8; 7]>",
				"line\r\nbreak",
			},
		},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			formatter := New()
			sample := &output.Sample{Frames: tt.frames, Count: 1}

			b.ReportAllocs()
			for b.Loop() {
				if err := formatter.Add(sample); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
