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
	"errors"
	"os"
	"path/filepath"
	"testing"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	"github.com/ccfos/huatuo/internal/profiler/output"
	_ "github.com/ccfos/huatuo/internal/profiler/output/raw"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestFormatterCombinesWindowsAndCompletesOnce(t *testing.T) {
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
	for range 2 {
		if err := formatter.Complete(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if string(readDestination(t, destination)) != "work 14\n" {
		t.Fatalf("output=%q", string(readDestination(t, destination)))
	}
	if err := formatter.Add(t.Context(), &types.ProfilingWindow{Profile: profile}); err == nil {
		t.Fatal("Add accepted after completion")
	}
}

func TestFormatterFailurePreventsCompletion(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profile")
	formatter := createFormatter(t, output.FormatCollapsed, destination)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := formatter.Add(ctx, &types.ProfilingWindow{Profile: &profilev1.Profile{}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := formatter.Complete(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed formatter wrote output")
	}
}

func createFormatter(t *testing.T, format output.OutputFormat, destination string) output.Formatter {
	t.Helper()
	formatter, cleanup, err := output.NewFormatter(output.FormatterConfig{Format: format, Destination: destination}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
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
	formatter, cleanup, err := output.NewFormatter(output.FormatterConfig{Format: output.FormatJSON, Destination: "stdout"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := formatter.Add(t.Context(), &types.ProfilingWindow{Profile: &profilev1.Profile{}}); err != nil {
		t.Fatal(err)
	}
	if err := formatter.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("still open"); err != nil {
		t.Fatal(err)
	}
}
