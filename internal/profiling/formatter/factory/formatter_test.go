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

package factory_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/chrometrace"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	"github.com/ccfos/huatuo/internal/profiling/formatter/flamegraph"
	jsonoutput "github.com/ccfos/huatuo/internal/profiling/formatter/json"
	"github.com/ccfos/huatuo/internal/profiling/formatter/raw"
	"github.com/ccfos/huatuo/internal/profiling/formatter/remotestore"
	"github.com/ccfos/huatuo/internal/profiling/formatter/speedscope"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestNewFormatterFormats(t *testing.T) {
	for _, tc := range []struct {
		format output.OutputFormat
		suffix string
	}{
		{"", ".folded"},
		{output.FormatCollapsed, ".folded"},
		{output.FormatFlameGraph, ".svg"},
		{output.FormatSVG, ".svg"},
		{output.FormatJSON, ".json"},
		{output.FormatSpeedscope, ".speedscope.json"},
		{output.FormatChromeTrace, ".chrometrace.json"},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			dir := t.TempDir()
			f, err := factory.NewFormatter(output.FormatterConfig{Format: tc.format, Destination: dir})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			})
			window := &types.ProfilingWindow{Profile: &profilev1.Profile{
				StringTable: []string{"", "samples", "count", "root"},
				SampleType:  []*profilev1.ValueType{{Type: 1, Unit: 2}},
				Function:    []*profilev1.Function{{Id: 1, Name: 3}},
				Location:    []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{{FunctionId: 1}}}},
				Sample:      []*profilev1.Sample{{LocationId: []uint64{1}, Value: []int64{1}}},
			}}
			if err := f.Add(t.Context(), window); err != nil {
				t.Fatal(err)
			}
			if err := f.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}
			paths, err := filepath.Glob(filepath.Join(dir, "*"+tc.suffix))
			if err != nil || len(paths) != 1 {
				t.Fatalf("output files: %v, %v", paths, err)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || len(data) == 0 {
				t.Fatalf("empty output: %v", err)
			}
		})
	}
}

func TestSubpackageConstructors(t *testing.T) {
	for _, tc := range []struct {
		name         string
		newFormatter func(output.FormatterConfig) (output.Formatter, error)
	}{
		{"raw", raw.NewFormatter},
		{"flamegraph", flamegraph.NewFormatter},
		{"json", jsonoutput.NewFormatter},
		{"speedscope", speedscope.NewFormatter},
		{"chrometrace", chrometrace.NewFormatter},
		{"remotestore", remotestore.NewFormatter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if f, err := tc.newFormatter(output.FormatterConfig{}); err == nil || f != nil {
				t.Fatal("empty destination accepted")
			}
			path := filepath.Join(t.TempDir(), "output")
			if tc.name == "remotestore" {
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			}
			f, err := tc.newFormatter(output.FormatterConfig{Destination: path, TaskID: "test", ToolName: "profiler"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNewFormatterRejectsUnknownFormat(t *testing.T) {
	f, err := factory.NewFormatter(output.FormatterConfig{Format: "unknown", Destination: "stdout"})
	if err == nil || f != nil {
		t.Fatalf("unknown format: %v, %v", f, err)
	}
}

func TestNewFormatterRemoteStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	f, err := factory.NewFormatter(output.FormatterConfig{Format: output.FormatRemoteStore, Destination: path, TaskID: "test", ToolName: "profiler"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFormattersPreserveUnsymbolizedStack(t *testing.T) {
	window := &types.ProfilingWindow{Profile: &profilev1.Profile{
		StringTable: []string{"", "samples", "count"},
		SampleType:  []*profilev1.ValueType{{Type: 1, Unit: 2}},
		Location:    []*profilev1.Location{{Id: 1, Address: 0x1234}},
		Sample:      []*profilev1.Sample{{LocationId: []uint64{1}, Value: []int64{7}}},
	}}
	for _, format := range []output.OutputFormat{
		output.FormatCollapsed, output.FormatSVG, output.FormatSpeedscope, output.FormatChromeTrace,
	} {
		t.Run(string(format), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile")
			formatter, err := factory.NewFormatter(output.FormatterConfig{Format: format, Destination: path})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := formatter.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := formatter.Add(t.Context(), window); err != nil {
				t.Fatal(err)
			}
			if err := formatter.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "0x1234") {
				t.Fatalf("address frame lost: %s", data)
			}
			if format == output.FormatCollapsed && string(data) != "0x1234 7\n" {
				t.Fatalf("weight changed: %s", data)
			}
		})
	}
}
