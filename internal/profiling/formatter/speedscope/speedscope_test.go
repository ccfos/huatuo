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

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

func testWindow(unit string, weight int64) *types.ProfilingWindow {
	return &types.ProfilingWindow{Profile: &profilev1.Profile{
		StringTable: []string{"", "cpu", unit, "root", "leaf", "main.go", "pid", "tid"},
		SampleType:  []*profilev1.ValueType{{Type: 1, Unit: 2}},
		Function:    []*profilev1.Function{{Id: 1, Name: 3, Filename: 5}, {Id: 2, Name: 4, Filename: 5}},
		Location:    []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{{FunctionId: 1, Line: 10}}}, {Id: 2, Line: []*profilev1.Line{{FunctionId: 2, Line: 20}}}},
		Sample:      []*profilev1.Sample{{LocationId: []uint64{2, 1}, Value: []int64{weight}}},
		Period:      10000000,
	}}
}

func testFormatter(t *testing.T) (output.Formatter, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.json")
	f, err := NewFormatter(output.FormatterConfig{Format: output.FormatSpeedscope, Destination: path})
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
	window.Profile.StringTable[3] = "mutated"
	window.Profile.Sample[0].Value[0] = 1
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
				window.Profile.Sample[0].Label = []*profilev1.Label{{Key: 6, Num: pid}, {Key: 7, Num: 1}}
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
		change func(*profilev1.Profile)
	}{
		{"invalid string table", func(p *profilev1.Profile) { p.StringTable[0] = "invalid" }},
		{"unsupported unit", func(p *profilev1.Profile) { p.StringTable[2] = "widgets" }},
		{"negative weight", func(p *profilev1.Profile) { p.Sample[0].Value[0] = -1 }},
		{"inexact weight", func(p *profilev1.Profile) { p.Sample[0].Value[0] = 1 << 53 }},
		{"missing sample type", func(p *profilev1.Profile) { p.SampleType = nil }},
		{"missing location", func(p *profilev1.Profile) { p.Sample[0].LocationId[0] = 99 }},
		{"missing function", func(p *profilev1.Profile) { p.Function = nil }},
		{"invalid string", func(p *profilev1.Profile) { p.Function[0].Name = 99 }},
		{"nil line", func(p *profilev1.Profile) { p.Location[0].Line[0] = nil }},
		{"nil label", func(p *profilev1.Profile) { p.Sample[0].Label = []*profilev1.Label{nil} }},
		{"duplicate location", func(p *profilev1.Profile) { p.Location = append(p.Location, p.Location[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, path := testFormatter(t)
			window := testWindow("bytes", 1)
			tc.change(window.Profile)
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
			f, err := NewFormatter(output.FormatterConfig{Format: output.FormatSpeedscope, Destination: dir})
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
