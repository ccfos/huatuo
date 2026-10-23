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
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"github.com/ccfos/huatuo/internal/profiler/memray"
	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/internal/profiling/window"
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

func TestMemrayDeltasRetainAllocationsAcrossBatches(t *testing.T) {
	aggr := aggregation.New()
	labels := []types.ProfileLabel{types.ProfilePIDLabel(123)}
	deliver := func(batch *pipeline.SampleBatch) error {
		for i := range batch.Samples {
			if err := aggr.Add(&batch.Samples[i]); err != nil {
				return err
			}
		}
		return nil
	}
	for _, deltas := range [][]memray.StackDelta{
		{{Frames: []string{"foo"}, Value: 10}, {Frames: []string{"gone"}, Value: 5}},
		{{Frames: []string{"foo"}, Value: -4}, {Frames: []string{"gone"}, Value: -5}},
	} {
		if err := enqueueMemrayDeltas(deltas, labels, deliver); err != nil {
			t.Fatal(err)
		}
	}
	profile := aggr.Drain()
	var retained int64
	for _, sample := range profile.Samples {
		retained += sample.Value
	}
	if retained != 6 {
		t.Fatalf("retained bytes = %d, want 6", retained)
	}
	want := errors.New("delivery failed")
	if err := enqueueMemrayDeltas([]memray.StackDelta{{Frames: []string{"foo"}, Value: -4}}, labels, func(*pipeline.SampleBatch) error {
		return want
	}); !errors.Is(err, want) {
		t.Fatalf("delivery error = %v, want %v", err, want)
	}
}

func TestMemrayFrameNamesSurviveAggregationAndOutput(t *testing.T) {
	paths := [][]string{{"root", "a;b"}, {"root", "a", "b"}, {"root", " a;b "}, {"root", "a:b"}, {"root", " \t "}}
	aggr := aggregation.New()
	deliver := func(batch *pipeline.SampleBatch) error {
		for i := range batch.Samples {
			if err := aggr.Add(&batch.Samples[i]); err != nil {
				return err
			}
		}
		return nil
	}
	for _, sign := range []int64{2, -1} {
		var deltas []memray.StackDelta
		for i, frames := range paths {
			deltas = append(deltas, memray.StackDelta{Frames: frames, Value: sign * int64(i+1) * 16})
		}
		if err := enqueueMemrayDeltas(deltas, nil, deliver); err != nil {
			t.Fatal(err)
		}
	}
	// Distinct decoder locations with identical frame names still merge here.
	if err := enqueueMemrayDeltas([]memray.StackDelta{{Frames: paths[0], Value: 8}}, nil, deliver); err != nil {
		t.Fatal(err)
	}
	w := &types.AggregationWindow{ProfileType: profiling.ProfileTypeInUseSpace, Aggregated: aggr.Drain()}
	p, err := window.BuildProfile(t.Context(), w)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Sample) != len(paths) || len(p.Function) != 7 {
		t.Fatalf("pprof has %d samples, %d functions; want %d, 7", len(p.Sample), len(p.Function), len(paths))
	}
	for i, expected := range paths {
		wantBytes := int64(i+1) * 16
		if i == 0 {
			wantBytes += 8
		}
		matches := 0
		for _, sample := range p.Sample {
			var names []string
			for j := len(sample.LocationId) - 1; j >= 0; j-- {
				loc := p.Location[sample.LocationId[j]-1]
				fn := p.Function[loc.Line[0].FunctionId-1]
				names = append(names, p.StringTable[fn.Name])
			}
			if slices.Equal(names, expected) && len(sample.Value) == 1 && sample.Value[0] == wantBytes {
				matches++
			}
		}
		if matches != 1 {
			t.Fatalf("pprof stack %q with %d bytes matched %d times", expected, wantBytes, matches)
		}
	}
	path := filepath.Join(t.TempDir(), "memory.svg")
	svg, err := factory.NewFormatter(output.Config{Format: output.FormatSVG, Destination: path})
	if err != nil {
		t.Fatal(err)
	}
	defer svg.Close()
	if err := svg.Add(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if err := svg.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantFrames := map[string]int64{"root": 248, "a;b": 24, "a": 32, "b": 32, " a;b ": 48, "a:b": 64, " \t ": 80}
	var document struct {
		Frames []struct {
			Class string `xml:"class,attr"`
			Title string `xml:"title"`
		} `xml:"g"`
	}
	if err := xml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, frame := range document.Frames {
		if frame.Class != "func_g" {
			continue
		}
		separator := strings.LastIndex(frame.Title, " (")
		if separator < 0 {
			t.Fatalf("SVG title has no byte count: %q", frame.Title)
		}
		name := frame.Title[:separator]
		wantBytes, ok := wantFrames[name]
		if !ok || !strings.HasPrefix(frame.Title[separator:], " ("+strconv.FormatInt(wantBytes, 10)+" bytes, ") {
			t.Fatalf("unexpected SVG frame %q", frame.Title)
		}
		delete(wantFrames, name)
	}
	if len(wantFrames) != 0 {
		t.Fatalf("SVG lost frames: %v", wantFrames)
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
