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

package provider

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestPythonSamplesOwnSourceFrames(t *testing.T) {
	var samples []aggregation.Sample
	enqueue := func(batch *pipeline.SampleBatch) error { samples = append(samples, batch.Samples...); return nil }
	err := enqueueFolded(
		profile.SampleOutput{
			PID:    100,
			Output: "worker (app.py:10) 3\nprocess 101:\"child\";work 2\n",
		},
		[]types.ProfileLabel{types.ProfilePIDLabel(100)},
		10000000,
		profiling.LanguagePython,
		enqueue,
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(samples) != 2 {
		t.Fatal(samples)
	}

	previous := samples[0]
	if err := enqueueFolded(
		profile.SampleOutput{
			PID:    100,
			Output: "worker (app.py:10) 9\n",
		},
		[]types.ProfileLabel{types.ProfilePIDLabel(100)},
		10000000,
		profiling.LanguagePython,
		enqueue,
	); err != nil {
		t.Fatal(err)
	}

	if previous.Value != 30000000 || previous.UserFrames[0].Line != 10 {
		t.Fatal("previous sample changed")
	}
}

func TestFoldedDeliveryPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, language := range []profiling.Language{profiling.LanguageJava, profiling.LanguagePython} {
		err := enqueueFolded(
			profile.SampleOutput{
				PID:    100,
				Output: "work 1\n",
			},
			nil,
			1,
			language,
			func(*pipeline.SampleBatch) error { return ctx.Err() },
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestFoldedSampleSyntax(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  string
		frames  []string
		value   int64
		wantErr bool
	}{
		{name: "frame with spaces", output: "root with spaces;leaf 2\n", frames: []string{"root with spaces", "leaf"}, value: 2},
		{name: "empty frames and signed weight", output: "root; ; leaf; -2\n", frames: []string{"root", "leaf"}, value: -2},
		{name: "missing weight separator", output: "root;leaf\n", wantErr: true},
		{name: "missing frames", output: " ; \t2\n", wantErr: true},
		{name: "invalid weight", output: "root;leaf invalid\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var samples []aggregation.Sample
			err := enqueueFolded(profile.SampleOutput{Output: tc.output}, nil, 1, profiling.LanguageJava, func(batch *pipeline.SampleBatch) error {
				samples = append(samples, batch.Samples...)
				return nil
			})
			if tc.wantErr {
				if err == nil || len(samples) != 0 {
					t.Fatalf("error=%v samples=%v", err, samples)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(samples) != 1 || samples[0].Value != tc.value {
				t.Fatalf("samples=%v", samples)
			}
			var names []string
			for _, frame := range samples[0].UserFrames {
				names = append(names, frame.Name)
			}
			if !slices.Equal(names, tc.frames) {
				t.Fatalf("frames=%v want=%v", names, tc.frames)
			}
		})
	}
}

func TestFoldedFramesPreserveFieldsAndOwnStrings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		language profiling.Language
		input    string
		frame    types.ProfileFrame
		isKernel bool
	}{
		{
			name:     "java user",
			language: profiling.LanguageJava,
			input:    "worker 3",
			frame:    types.ProfileFrame{Name: "worker"},
		},
		{
			name:     "java kernel",
			language: profiling.LanguageJava,
			input:    "worker_[k] 3",
			frame:    types.ProfileFrame{Name: "worker"},
			isKernel: true,
		},
		{
			name:     "java allocation type",
			language: profiling.LanguageJava,
			input:    "byte[] 3",
			frame:    types.ProfileFrame{Name: "byte[]"},
		},
		{
			name:     "python source",
			language: profiling.LanguagePython,
			input:    "worker (app.py:10) 3",
			frame: types.ProfileFrame{
				Name: "worker",
				File: "app.py",
				Line: 10,
			},
		},
		{
			name:     "python plain",
			language: profiling.LanguagePython,
			input:    "worker 3",
			frame:    types.ProfileFrame{Name: "worker"},
		},
		{
			name:     "python without line",
			language: profiling.LanguagePython,
			input:    "worker (app.py) 3",
			frame:    types.ProfileFrame{Name: "worker (app.py)"},
		},
		{
			name:     "python invalid line",
			language: profiling.LanguagePython,
			input:    "worker (app.py:bad) 3",
			frame:    types.ProfileFrame{Name: "worker (app.py:bad)"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.Clone(tc.input)
			var samples []aggregation.Sample
			err := enqueueFolded(profile.SampleOutput{Output: input}, nil, 1, tc.language, func(batch *pipeline.SampleBatch) error {
				samples = append(samples, batch.Samples...)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(samples) != 1 || samples[0].Value != 3 {
				t.Fatalf("samples = %v", samples)
			}

			frames, other := samples[0].UserFrames, samples[0].KernelFrames
			if tc.isKernel {
				frames, other = other, frames
			}
			if !slices.Equal(frames, []types.ProfileFrame{tc.frame}) || len(other) != 0 {
				t.Fatalf("user frames = %v, kernel frames = %v", samples[0].UserFrames, samples[0].KernelFrames)
			}

			for _, value := range []string{frames[0].Name, frames[0].File} {
				if value == "" {
					continue
				}
				offset := strings.Index(input, value)
				if offset < 0 {
					t.Fatalf("fixture does not contain retained value %q", value)
				}
				if unsafe.StringData(value) == unsafe.StringData(input[offset:]) {
					t.Fatalf("retained value %q still references the input buffer", value)
				}
			}
		})
	}
}
