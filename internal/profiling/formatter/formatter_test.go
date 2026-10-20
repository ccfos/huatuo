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

package formatter_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestFormatterCollapsedOutput(t *testing.T) {
	window := &types.AggregationWindow{
		ProfileType: profiling.ProfileTypeIRQTracingSample,
		Aggregated: types.AggregatedProfile{
			Frames: []types.ProfileFrame{{Name: "source"}, {Name: "raise;net\nrx"}, {Name: "raise:net rx"}},
			Stacks: [][]uint32{{0, 1}, {0, 2}},
			Samples: []types.AggregatedSample{
				{UserStack: 1, Value: 3},
				{UserStack: 2, Value: 2},
			},
		},
	}
	destination := filepath.Join(t.TempDir(), "profile")
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
	for _, format := range []output.OutputFormat{output.FormatFlameGraph, output.FormatSVG} {
		t.Run(string(format), func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "profile")
			window := sampleWindow(3, "source", "raise_softirq_[k]")
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
	window := &types.AggregationWindow{
		ProfileType:    profiling.ProfileTypeCpuSample,
		DroppedSamples: map[string]uint64{types.DropReasonCollection: 3},
	}
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
	window := sampleWindow(7, "work")
	for range 2 {
		if err := formatter.Add(t.Context(), window); err != nil {
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
	for _, format := range []output.OutputFormat{
		output.FormatCollapsed,
		output.FormatSVG,
		output.FormatJSON,
		output.FormatSpeedscope,
		output.FormatChromeTrace,
	} {
		t.Run(string(format), func(t *testing.T) {
			for _, operation := range []string{"add", "complete"} {
				t.Run(operation, func(t *testing.T) {
					destination := filepath.Join(t.TempDir(), "profile")
					formatter := createFormatter(t, format, destination)
					ctx, cancel := context.WithCancel(t.Context())
					cancel()
					var err error
					if operation == "add" {
						err = formatter.Add(ctx, &types.AggregationWindow{ProfileType: profiling.ProfileTypeCpuSample})
					} else {
						err = formatter.Complete(ctx)
					}

					//nolint:errorlint // Verify the exact Formatter cancellation contract.
					if err != context.Canceled {
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
	formatter, err := factory.NewFormatter(output.Config{
		Format:      format,
		Destination: destination,
	})
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
	formatter, err := factory.NewFormatter(output.Config{
		Format:      output.FormatJSON,
		Destination: "stdout",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := formatter.Add(t.Context(), &types.AggregationWindow{ProfileType: profiling.ProfileTypeCpuSample}); err != nil {
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
			window := sampleWindow(4096, "original")
			if err := f.Add(t.Context(), window); err != nil {
				t.Fatal(err)
			}

			window.Aggregated.Frames[0].Name = "mutated"
			window.Aggregated.Samples[0].Value = 1
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

func sampleWindow(value int64, names ...string) *types.AggregationWindow {
	frames := make([]types.ProfileFrame, len(names))
	stack := make([]uint32, len(names))
	for i, name := range names {
		frames[i] = types.ProfileFrame{Name: name}
		stack[i] = uint32(i)
	}

	return &types.AggregationWindow{
		ProfileType: profiling.ProfileTypeIRQTracingSample,
		Aggregated: types.AggregatedProfile{
			Frames:  frames,
			Stacks:  [][]uint32{stack},
			Samples: []types.AggregatedSample{{UserStack: 1, Value: value}},
		},
	}
}

func TestSummaryFormattersAccumulateSignedFullIdentities(t *testing.T) {
	for _, format := range []output.OutputFormat{output.FormatCollapsed, output.FormatSVG, output.FormatSpeedscope} {
		t.Run(string(format), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile")
			f := createFormatter(t, format, path)
			for _, value := range []int64{-4096, 8192, -4096} {
				w := sampleWindow(value, "work")
				if err := f.Add(t.Context(), w); err != nil {
					t.Fatal(err)
				}
			}
			// Identical display names with different source locations must not cancel
			// before negative results are clamped at completion.
			w := sampleWindow(8192, "other")
			w.Aggregated.Frames[0].File = "positive.go"
			if err := f.Add(t.Context(), w); err != nil {
				t.Fatal(err)
			}

			w.Aggregated.Frames[0].File = "negative.go"
			w.Aggregated.Samples[0].Value = -4096
			if err := f.Add(t.Context(), w); err != nil {
				t.Fatal(err)
			}

			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("published before Complete")
			}

			if err := f.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}

			data := string(readDestination(t, path))
			switch format {
			case output.FormatCollapsed:
				if data != "other 8192\n" {
					t.Fatalf("weights: %q", data)
				}
			case output.FormatSVG:
				if !strings.Contains(data, "8192") {
					t.Fatal("live weight missing")
				}
			case output.FormatSpeedscope:
				var result struct {
					Profiles []struct {
						EndValue int64 `json:"endValue"`
					} `json:"profiles"`
				}

				if err := json.Unmarshal([]byte(data), &result); err != nil {
					t.Fatal(err)
				}

				if len(result.Profiles) != 1 || result.Profiles[0].EndValue != 8192 {
					t.Fatalf("weights: %+v", result)
				}
			}
		})
	}
}

func TestSummaryFormattersPublishCanceledSamples(t *testing.T) {
	for _, format := range []output.OutputFormat{output.FormatCollapsed, output.FormatSVG, output.FormatSpeedscope} {
		t.Run(string(format), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile")
			f := createFormatter(t, format, path)
			for _, value := range []int64{4096, -4096} {
				if err := f.Add(t.Context(), sampleWindow(value, "work")); err != nil {
					t.Fatal(err)
				}
			}

			if err := f.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}

			if _, err := os.Stat(path); err != nil {
				t.Fatalf("zero result missing: %v", err)
			}
		})
	}
}

func TestSVGCancelingDeepStacksDoesNotIncreaseHeight(t *testing.T) {
	var heights []int
	for _, canceled := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "profile.svg")
		f := createFormatter(t, output.FormatSVG, path)
		if canceled {
			for _, value := range []int64{7, -7} {
				if err := f.Add(t.Context(), sampleWindow(value, "root", "deep", "leaf")); err != nil {
					t.Fatal(err)
				}
			}
		}

		if err := f.Add(t.Context(), sampleWindow(1, "other")); err != nil {
			t.Fatal(err)
		}

		if err := f.Complete(t.Context()); err != nil {
			t.Fatal(err)
		}

		data := readDestination(t, path)
		var svg struct {
			Height int `xml:"height,attr"`
		}
		if err := xml.Unmarshal(data, &svg); err != nil {
			t.Fatal(err)
		}

		if strings.Contains(string(data), `data-title="deep`) {
			t.Fatal("canceled stack rendered")
		}

		heights = append(heights, svg.Height)
	}

	if heights[0] == 0 || heights[0] != heights[1] {
		t.Fatalf("canceled stack changed height: %v", heights)
	}
}
