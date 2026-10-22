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

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/chrometrace"
	"github.com/ccfos/huatuo/internal/profiling/formatter/collapsed"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	"github.com/ccfos/huatuo/internal/profiling/formatter/flamegraph"
	jsonoutput "github.com/ccfos/huatuo/internal/profiling/formatter/json"
	"github.com/ccfos/huatuo/internal/profiling/formatter/remotestore"
	"github.com/ccfos/huatuo/internal/profiling/formatter/speedscope"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestNewFormatterFormats(t *testing.T) {
	for _, tc := range []struct {
		format output.OutputFormat
		name   output.OutputFormat
		suffix string
	}{
		{
			format: "",
			name:   output.FormatCollapsed,
			suffix: ".folded",
		},
		{
			format: output.FormatCollapsed,
			name:   output.FormatCollapsed,
			suffix: ".folded",
		},
		{
			format: output.FormatFlameGraph,
			name:   output.FormatSVG,
			suffix: ".svg",
		},
		{
			format: output.FormatSVG,
			name:   output.FormatSVG,
			suffix: ".svg",
		},
		{
			format: output.FormatJSON,
			name:   output.FormatJSON,
			suffix: ".json",
		},
		{
			format: output.FormatSpeedscope,
			name:   output.FormatSpeedscope,
			suffix: ".speedscope.json",
		},
		{
			format: output.FormatChromeTrace,
			name:   output.FormatChromeTrace,
			suffix: ".chrometrace.json",
		},
	} {
		t.Run(string(tc.format), func(t *testing.T) {
			dir := t.TempDir()
			f, err := factory.NewFormatter(output.Config{
				Format:      tc.format,
				Destination: dir,
			})
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			})
			if got := f.Name(); got != tc.name {
				t.Fatalf("format name = %q, want %q", got, tc.name)
			}

			window := &types.AggregationWindow{
				ProfileType: "profiler:cpu:nanoseconds:cpu:nanoseconds",
				Aggregated: types.AggregatedProfile{
					Frames: []types.ProfileFrame{{Name: "root"}},
					Stacks: [][]uint32{{0}},
					Samples: []types.AggregatedSample{{
						UserStack: 1,
						Value:     1,
					}},
				},
			}
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
		newFormatter func(output.Config) (output.Formatter, error)
	}{
		{
			name:         "collapsed",
			newFormatter: collapsed.NewFormatter,
		},
		{
			name:         "flamegraph",
			newFormatter: flamegraph.NewFormatter,
		},
		{
			name:         "json",
			newFormatter: jsonoutput.NewFormatter,
		},
		{
			name:         "speedscope",
			newFormatter: speedscope.NewFormatter,
		},
		{
			name:         "chrometrace",
			newFormatter: chrometrace.NewFormatter,
		},
		{
			name:         "remotestore",
			newFormatter: remotestore.NewFormatter,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if f, err := tc.newFormatter(output.Config{}); err == nil || f != nil {
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

			f, err := tc.newFormatter(output.Config{
				Destination: path,
				TaskID:      "test",
				ToolName:    "profiler",
			})
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
	f, err := factory.NewFormatter(output.Config{
		Format:      "unknown",
		Destination: "stdout",
	})
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
	f, err := factory.NewFormatter(output.Config{
		Format:      output.FormatRemoteStore,
		Destination: path,
		TaskID:      "test",
		ToolName:    "profiler",
	})
	if err != nil {
		t.Fatal(err)
	}

	if f.Name() != output.FormatRemoteStore {
		t.Fatalf("remote formatter name = %q", f.Name())
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFormattersPreserveUnsymbolizedStack(t *testing.T) {
	window := &types.AggregationWindow{
		ProfileType: "profiler:cpu:nanoseconds:cpu:nanoseconds",
		Aggregated: types.AggregatedProfile{
			Frames: []types.ProfileFrame{{Name: "0x1234"}},
			Stacks: [][]uint32{{0}},
			Samples: []types.AggregatedSample{{
				UserStack: 1,
				Value:     7,
			}},
		},
	}
	for _, format := range []output.OutputFormat{
		output.FormatCollapsed, output.FormatSVG, output.FormatSpeedscope, output.FormatChromeTrace,
	} {
		t.Run(string(format), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profile")
			formatter, err := factory.NewFormatter(output.Config{
				Format:      format,
				Destination: path,
			})
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
