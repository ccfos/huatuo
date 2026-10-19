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

package speedscope

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

func testWindow(unit string, weight int64) *types.AggregationWindow {
	return &types.AggregationWindow{
		ProfileType: "process_cpu:cpu:" + unit + ":cpu:" + unit,
		Aggregated: types.AggregatedProfile{
			Frames: []types.ProfileFrame{{
				Name: "root",
				File: "main.go",
				Line: 10,
			}, {
				Name: "leaf",
				File: "main.go",
				Line: 20,
			}},
			Stacks: [][]uint32{{0, 1}},
			Samples: []types.AggregatedSample{{
				UserStack: 1,
				Value:     weight,
			}},
		},
	}
}

func testFormatter(t *testing.T) (output.Formatter, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.json")
	f, err := NewFormatter(output.Config{
		Format:      output.FormatSpeedscope,
		Destination: path,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})

	return f, path
}

func readFile(t *testing.T, path string) speedscopeFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var file speedscopeFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}

	return file
}

func TestFormatterWeightsAndOwnership(t *testing.T) {
	f, path := testFormatter(t)
	window := testWindow("nanoseconds", 7000000000)
	for range 2 {
		if err := f.Add(t.Context(), window); err != nil {
			t.Fatal(err)
		}
	}

	window.Aggregated.Frames[0].Name = "mutated"
	window.Aggregated.Samples[0].Value = 1
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("output created before completion")
	}

	if err := f.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}

	file := readFile(t, path)
	if len(file.Profiles) != 1 || len(file.Shared.Frames) != 2 {
		t.Fatalf("file: %+v", file)
	}

	p := file.Profiles[0]
	if len(p.Samples) != 2 || p.Unit != "nanoseconds" || p.EndValue != 14000000000 || p.Weights[0] != 7000000000 {
		t.Fatalf("weights expanded or rescaled: %+v", p)
	}

	if file.Shared.Frames[p.Samples[0][0]].Name != "root" || file.Shared.Frames[p.Samples[0][1]].Line != 20 {
		t.Fatalf("stack order or source location changed: %+v", file.Shared.Frames)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	want, err := os.ReadFile("testdata/weighted.speedscope.json")
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("output differs from viewer fixture:\n%s", got)
	}
}

func TestFormatterUnitsAndGroups(t *testing.T) {
	for _, unit := range []string{"bytes", "count", "nanoseconds"} {
		t.Run(unit, func(t *testing.T) {
			f, path := testFormatter(t)
			for pid := int64(1); pid <= 2; pid++ {
				window := testWindow(unit, pid*3)
				window.Aggregated.LabelSets = [][]types.ProfileLabel{{types.ProfilePIDLabel(uint32(pid)), {
					Name:        "tid",
					Kind:        types.ProfileLabelNumber,
					NumberValue: 1,
				}}}
				window.Aggregated.Samples[0].LabelSet = 1
				if err := f.Add(t.Context(), window); err != nil {
					t.Fatal(err)
				}
			}

			if err := f.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}

			file := readFile(t, path)
			if len(file.Profiles) != 2 || file.ActiveProfileIdx != 1 {
				t.Fatalf("process groups merged: %+v", file.Profiles)
			}

			wantUnit := unit
			if unit == "count" {
				wantUnit = "none"
			}

			for _, p := range file.Profiles {
				if p.Unit != wantUnit || !strings.Contains(p.Name, "pid") || !strings.Contains(p.Name, "tid") {
					t.Fatalf("group metadata: %+v", p)
				}
			}
		})
	}
}

func TestFormatterRejectsInvalidProfiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*types.AggregationWindow)
	}{
		{
			name:   "unsupported unit",
			change: func(w *types.AggregationWindow) { w.ProfileType = "process_cpu:cpu:widgets:cpu:widgets" },
		},
		{
			name:   "negative weight",
			change: func(w *types.AggregationWindow) { w.Aggregated.Samples[0].Value = -1 },
		},
		{
			name:   "inexact weight",
			change: func(w *types.AggregationWindow) { w.Aggregated.Samples[0].Value = 1 << 53 },
		},
		{
			name:   "missing sample type",
			change: func(w *types.AggregationWindow) { w.ProfileType = "" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, path := testFormatter(t)
			window := testWindow("bytes", 1)
			tc.change(window)
			if err := f.Add(t.Context(), window); err == nil {
				t.Fatal("invalid profile accepted")
			}

			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("failed task created output")
			}
		})
	}
}

func TestFormatterRejectsMixedUnitsAndOverflow(t *testing.T) {
	for _, unit := range []string{"bytes", "nanoseconds"} {
		t.Run(unit, func(t *testing.T) {
			f, _ := testFormatter(t)
			if err := f.Add(t.Context(), testWindow("bytes", maxExactInteger)); err != nil {
				t.Fatal(err)
			}

			if err := f.Add(t.Context(), testWindow(unit, 1)); err == nil {
				t.Fatal("accepted mixed unit or overflowing total")
			}
		})
	}
}

func TestFormatterEmptyAndDirectory(t *testing.T) {
	for _, weight := range []int64{0, 3} {
		t.Run(string(rune('0'+weight)), func(t *testing.T) {
			dir := t.TempDir()
			f, err := NewFormatter(output.Config{
				Format:      output.FormatSpeedscope,
				Destination: dir,
			})
			if err != nil {
				t.Fatal(err)
			}

			defer f.Close()
			if err := f.Add(t.Context(), testWindow("count", weight)); err != nil {
				t.Fatal(err)
			}

			if err := f.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}

			files, err := filepath.Glob(filepath.Join(dir, "*.speedscope.json"))
			if err != nil {
				t.Fatal(err)
			}

			if (weight == 0 && len(files) != 0) || (weight != 0 && len(files) != 1) {
				t.Fatalf("files: %v", files)
			}
		})
	}
}

func TestAggregatedZeroWeightsDoNotCreateOutput(t *testing.T) {
	f, path := testFormatter(t)
	w := &types.AggregationWindow{
		ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
		Aggregated: types.AggregatedProfile{
			Frames:  []types.ProfileFrame{{Name: "work"}},
			Stacks:  [][]uint32{{0}},
			Samples: []types.AggregatedSample{{UserStack: 1}},
		},
	}
	if err := f.Add(t.Context(), w); err != nil {
		t.Fatal(err)
	}

	if err := f.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("zero-weight window created output: %v", err)
	}
}
