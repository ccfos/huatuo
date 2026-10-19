// Copyright 2025, 2026 The HuaTuo Authors
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
	"bytes"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/profiler"
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
)

func TestLocalAggregatorsReleaseRawWindows(t *testing.T) {
	for _, language := range []string{"java", "python"} {
		t.Run(language, func(t *testing.T) {
			ctx := &pcontext.ProfilerContext{OutputFormat: output.FormatCollapsed}
			java, err := newJavaAggregator(ctx)
			if err != nil {
				t.Fatal(err)
			}
			python, err := newPythonCPUAggregator(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 100; i++ {
				sample := profiler.SampleOutput{PID: 100, Output: strings.Clone("hot 3\n")}
				if language == "java" {
					java.Aggregate(sample)
				} else {
					python.Aggregate(sample)
				}
			}
			var data bytes.Buffer
			var rawCount int
			if language == "java" {
				err = java.OutputFormatter().Write(&data)
				rawCount = len(java.sampleOutput)
			} else {
				err = python.OutputFormatter().Write(&data)
				rawCount = len(python.sampleOutput)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := data.String(); got != "process 100;hot 300\n" {
				t.Fatalf("folded=%q", got)
			}
			if rawCount != 0 {
				t.Fatalf("kept %d raw windows after local formatting", rawCount)
			}
		})
	}
}

func TestRemoteAggregatorsRetainRawWindows(t *testing.T) {
	ctx := &pcontext.ProfilerContext{OutputFormat: output.FormatRemote}
	java, err := newJavaAggregator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	python, err := newPythonCPUAggregator(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sample := profiler.SampleOutput{PID: 100, Output: "hot 3\n"}
	java.Aggregate(sample)
	python.Aggregate(sample)
	if len(java.sampleOutput) != 1 || len(python.sampleOutput) != 1 {
		t.Fatal("remote output lost its raw input")
	}
}

func BenchmarkLocalJavaAggregationWindows(b *testing.B) {
	var retained int
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		a, err := newJavaAggregator(&pcontext.ProfilerContext{OutputFormat: output.FormatCollapsed})
		if err != nil {
			b.Fatal(err)
		}
		for j := 0; j < 100; j++ {
			a.Aggregate(profiler.SampleOutput{PID: 100, Output: strings.Clone("hot 3\n")})
		}
		retained = 0
		for _, sample := range a.sampleOutput {
			retained += len(sample.Output)
		}
	}
	b.ReportMetric(float64(retained), "retained_raw_B/op")
}
