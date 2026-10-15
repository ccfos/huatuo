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

package output_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	"github.com/ccfos/huatuo/internal/profiler"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/profiler/output/factory"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestFormatterCollapsedOutput(t *testing.T) {
	items := []*profiler.TreeItem{
		{Stack: [][]byte{[]byte("source"), []byte("raise;net\nrx")}, Value: 3},
		{Stack: [][]byte{[]byte("source"), []byte("raise:net rx")}, Value: 2},
	}

	destination := filepath.Join(t.TempDir(), "profile")
	data, err := profiler.ParseTree(time.Now(), profiler.ProfileTypeIRQTracingSample, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	window, err := profiler.NewWindow(data, "")
	if err != nil {
		t.Fatal(err)
	}
	formatter := createFormatter(t, output.FormatCollapsed, destination)
	if err := formatter.Add(t.Context(), window); err != nil {
		t.Fatalf("Formatter.Add() error = %v", err)
	}
	if err := formatter.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := string(readDestination(t, destination)); got != "source;raise:net rx 5\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestFormatterFlameGraphOutput(t *testing.T) {
	items := []*profiler.TreeItem{
		{Stack: [][]byte{[]byte("source"), []byte("raise_softirq_[k]")}, Value: 3},
	}

	for _, format := range []output.OutputFormat{output.FormatFlameGraph, output.FormatSVG} {
		t.Run(string(format), func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile")
			data, err := profiler.ParseTree(time.Now(), profiler.ProfileTypeIRQTracingSample, items, nil)
			if err != nil {
				t.Fatal(err)
			}
			window, err := profiler.NewWindow(data, "")
			if err != nil {
				t.Fatal(err)
			}
			formatter := createFormatter(t, format, destination)
			if err := formatter.Add(t.Context(), window); err != nil {
				t.Fatalf("Formatter.Add() error = %v", err)
			}
			if err := formatter.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := string(readDestination(t, destination)); !strings.Contains(got, "<svg") ||
				!strings.Contains(got, "raise_softirq_[k]") {
				t.Fatalf("output does not contain an SVG flame graph: %q", got)
			}
		})
	}
}

func TestFormatterJSONOutput(t *testing.T) {
	window := &types.ProfilingWindow{Profile: &profilev1.Profile{}, DroppedSamples: map[string]uint64{types.DropReasonCollection: 3}}
	destination := filepath.Join(t.TempDir(), "profile")
	formatter := createFormatter(t, output.FormatJSON, destination)
	if err := formatter.Add(t.Context(), window); err != nil {
		t.Fatal(err)
	}
	var got types.ProfilingWindow
	if err := json.Unmarshal(readDestination(t, destination), &got); err != nil {
		t.Fatal(err)
	}
	if got.DroppedSamples[types.DropReasonCollection] != 3 {
		t.Fatalf("window = %+v", got)
	}
}

func TestFormatterCombinesWindows(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	formatter := createFormatter(t, output.FormatCollapsed, destination)
	profile := &profilev1.Profile{
		StringTable: []string{"", "work"},
		Function:    []*profilev1.Function{{Id: 1, Name: 1}},
		Location:    []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{{FunctionId: 1}}}},
		Sample:      []*profilev1.Sample{{LocationId: []uint64{1}, Value: []int64{7}}},
	}
	for range 2 {
		if err := formatter.Add(t.Context(), &types.ProfilingWindow{Profile: profile}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("output finalized before Complete")
	}
	if err := formatter.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	if string(readDestination(t, destination)) != "work 14\n" {
		t.Fatalf("output=%q", string(readDestination(t, destination)))
	}
}

func TestFormatterCancellationDoesNotCreateFile(t *testing.T) {
	for _, format := range []output.OutputFormat{output.FormatCollapsed, output.FormatSVG, output.FormatJSON, output.FormatSpeedscope, output.FormatChromeTrace} {
		t.Run(string(format), func(t *testing.T) {
			for _, operation := range []string{"add", "complete"} {
				t.Run(operation, func(t *testing.T) {
					destination := filepath.Join(t.TempDir(), "profile")
					formatter := createFormatter(t, format, destination)
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					var err error
					if operation == "add" {
						err = formatter.Add(ctx, &types.ProfilingWindow{Profile: &profilev1.Profile{}})
					} else {
						err = formatter.Complete(ctx)
					}
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("%s error = %v", operation, err)
					}
					if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("canceled output created a file")
					}
				})
			}
		})
	}
}

func createFormatter(t *testing.T, format output.OutputFormat, destination string) output.Formatter {
	t.Helper()
	formatter, err := factory.NewFormatter(output.FormatterConfig{Format: format, Destination: destination})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := formatter.Close(); err != nil {
			t.Error(err)
		}
	})
	return formatter
}

func readDestination(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFormatterEmptyCollectionDoesNotCreateFile(t *testing.T) {
	for _, format := range []output.OutputFormat{output.FormatCollapsed, output.FormatSVG, output.FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile")
			formatter := createFormatter(t, format, path)
			if err := formatter.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("empty output created a file: %v", err)
			}
		})
	}
}

func TestFormatterStandardOutputIsNotClosed(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	previous := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = previous }()
	formatter, err := factory.NewFormatter(output.FormatterConfig{Format: output.FormatJSON, Destination: "stdout"})
	if err != nil {
		t.Fatal(err)
	}
	if err := formatter.Add(t.Context(), &types.ProfilingWindow{Profile: &profilev1.Profile{}}); err != nil {
		t.Fatal(err)
	}
	if err := formatter.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := formatter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("still open"); err != nil {
		t.Fatal(err)
	}
}

func TestFormatterOwnsAccumulatedValues(t *testing.T) {
	for _, format := range []output.OutputFormat{"", output.FormatSVG} {
		t.Run(string(format), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile")
			f := createFormatter(t, format, path)
			profile := &profilev1.Profile{
				StringTable: []string{"", "original"},
				Function:    []*profilev1.Function{{Id: 1, Name: 1}},
				Location:    []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{{FunctionId: 1}}}},
				Sample:      []*profilev1.Sample{{LocationId: []uint64{1}, Value: []int64{4096, 99}}},
			}
			if err := f.Add(t.Context(), &types.ProfilingWindow{Profile: profile}); err != nil {
				t.Fatal(err)
			}
			profile.StringTable[1] = "mutated"
			profile.Sample[0].Value[0] = 1
			if err := f.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := string(readDestination(t, path))
			if !strings.Contains(got, "original") || strings.Contains(got, "mutated") {
				t.Fatalf("retained caller data: %s", got)
			}
			if format == "" && got != "original 4096\n" {
				t.Fatalf("stack weight changed: %q", got)
			}
		})
	}
}
