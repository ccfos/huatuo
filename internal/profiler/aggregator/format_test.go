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
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	"github.com/ccfos/huatuo/internal/profiler"
	profctx "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestCollapsedWriter(t *testing.T) {
	items := []*profiler.TreeItem{
		{Stack: [][]byte{[]byte("source"), []byte("raise;net\nrx")}, Value: 3},
		{Stack: [][]byte{[]byte("source"), []byte("raise:net rx")}, Value: 2},
	}

	for _, format := range []output.OutputFormat{output.FormatCollapsed} {
		t.Run(string(format), func(t *testing.T) {
			var destination bytes.Buffer
			data, err := profiler.ParseTree(time.Now(), profiler.ProfileTypeIRQTracingSample, items, nil)
			if err != nil {
				t.Fatal(err)
			}
			window, err := profiler.NewWindow(data, "", time.Second)
			p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context(), OutputFormat: format, OutputWriter: &destination}, nil)
			if err != nil {
				t.Fatalf("newWriter() error = %v", err)
			}
			if err := p.exportWindow(window); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if err := p.formatter.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := destination.String(); got != "source;raise:net rx 5\n" {
				t.Fatalf("output = %q", got)
			}
		})
	}
}

func TestFlameGraphWriter(t *testing.T) {
	items := []*profiler.TreeItem{
		{Stack: [][]byte{[]byte("source"), []byte("raise_softirq_[k]")}, Value: 3},
	}

	for _, format := range []output.OutputFormat{output.FormatFlameGraph, output.FormatSVG} {
		t.Run(string(format), func(t *testing.T) {
			var destination bytes.Buffer
			data, err := profiler.ParseTree(time.Now(), profiler.ProfileTypeIRQTracingSample, items, nil)
			if err != nil {
				t.Fatal(err)
			}
			window, err := profiler.NewWindow(data, "", time.Second)
			p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context(), OutputFormat: format, OutputWriter: &destination}, nil)
			if err != nil {
				t.Fatalf("newWriter() error = %v", err)
			}
			if err := p.exportWindow(window); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
			if err := p.formatter.Complete(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := destination.String(); !strings.Contains(got, "<svg") ||
				!strings.Contains(got, "raise_softirq_[k]") {
				t.Fatalf("output does not contain an SVG flame graph: %q", got)
			}
		})
	}
}

func TestJSONWriter(t *testing.T) {
	window := &types.ProfilingWindow{Profile: &profilev1.Profile{}, DroppedSamples: map[string]uint64{types.DropReasonCollection: 3}}
	var destination bytes.Buffer
	p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context(), OutputFormat: output.FormatJSON, OutputWriter: &destination}, nil)
	if err := p.exportWindow(window); err != nil {
		t.Fatal(err)
	}
	var got types.ProfilingWindow
	if err := json.Unmarshal(destination.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.DroppedSamples[types.DropReasonCollection] != 3 {
		t.Fatalf("window = %+v", got)
	}
}
