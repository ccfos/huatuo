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

package aggregator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/profiler/output/chrometrace"
	"github.com/ccfos/huatuo/internal/profiler/output/dump"
	"github.com/ccfos/huatuo/internal/profiler/output/flamegraph"
	"github.com/ccfos/huatuo/internal/profiler/output/raw"
	"github.com/ccfos/huatuo/internal/profiler/output/speedscope"
)

func TestCreateOutputFileUsesUniqueNames(t *testing.T) {
	dir := t.TempDir()

	first, err := createOutputFile(dir, "perf", ".folded")
	if err != nil {
		t.Fatalf("create first output file: %v", err)
	}
	firstName := first.Name()
	if _, err := first.WriteString("first profile"); err != nil {
		t.Fatalf("write first output file: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first output file: %v", err)
	}

	second, err := createOutputFile(dir, "perf", ".folded")
	if err != nil {
		t.Fatalf("create second output file: %v", err)
	}
	secondName := second.Name()
	if err := second.Close(); err != nil {
		t.Fatalf("close second output file: %v", err)
	}

	if firstName == secondName {
		t.Fatalf("createOutputFile reused %q", firstName)
	}
	for _, name := range []string{firstName, secondName} {
		base := filepath.Base(name)
		if !strings.HasPrefix(base, "perf_") || !strings.HasSuffix(base, ".folded") {
			t.Errorf("output filename %q does not preserve prefix and extension", base)
		}
	}

	data, err := os.ReadFile(firstName)
	if err != nil {
		t.Fatalf("read first output file: %v", err)
	}
	if got, want := string(data), "first profile"; got != want {
		t.Errorf("first output content = %q, want %q", got, want)
	}
}

func TestCreateOutputFileCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "profiles")

	file, err := createOutputFile(dir, "flamegraph", ".svg")
	if err != nil {
		t.Fatalf("create output file: %v", err)
	}

	if got := filepath.Dir(file.Name()); got != dir {
		t.Errorf("output directory = %q, want %q", got, dir)
	}
	if base := filepath.Base(file.Name()); !strings.HasPrefix(base, "flamegraph_") || !strings.HasSuffix(base, ".svg") {
		t.Errorf("output filename %q does not preserve prefix and extension", base)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close output file: %v", err)
	}
}

func TestWriteOutputUsesFormatSpecificArtifact(t *testing.T) {
	tests := []struct {
		format    output.OutputFormat
		prefix    string
		ext       string
		formatter output.Formatter
	}{
		{output.FormatCollapsed, "perf_", ".folded", raw.New()},
		{output.FormatFlameGraph, "flamegraph_", ".svg", flamegraph.New()},
		{output.FormatSVG, "flamegraph_", ".svg", flamegraph.New()},
		{output.FormatSpeedscope, "speedscope_", ".json", speedscope.New(0)},
		{output.FormatChromeTrace, "chrometrace_", ".json", chrometrace.New(0)},
		{output.FormatDump, "perf_", ".txt", dump.New(dump.Options{})},
	}

	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			dir := t.TempDir()
			if err := writeOutput(dir, tt.format, tt.formatter); err != nil {
				t.Fatalf("writeOutput(%q) error = %v", tt.format, err)
			}

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read output directory: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("output files = %d, want 1", len(entries))
			}

			name := entries[0].Name()
			if !strings.HasPrefix(name, tt.prefix) || !strings.HasSuffix(name, tt.ext) {
				t.Fatalf("output file %q does not match prefix %q and extension %q", name, tt.prefix, tt.ext)
			}
		})
	}
}

func TestWriteOutputTreatsEmptyFormatAsCollapsed(t *testing.T) {
	dir := t.TempDir()
	if err := writeOutput(dir, "", raw.New()); err != nil {
		t.Fatalf("writeOutput(\"\") error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read output directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("output files = %d, want 1", len(entries))
	}
	if name := entries[0].Name(); !strings.HasPrefix(name, "perf_") || !strings.HasSuffix(name, ".folded") {
		t.Fatalf("zero-value format wrote %q, want a folded artifact", name)
	}
}

func TestWriteOutputRejectsUnsupportedFormat(t *testing.T) {
	for _, format := range []output.OutputFormat{output.FormatRemote, output.FormatPprof} {
		err := writeOutput(t.TempDir(), format, raw.New())
		if err == nil {
			t.Fatalf("writeOutput(%q) returned nil error", format)
		}
	}
}
