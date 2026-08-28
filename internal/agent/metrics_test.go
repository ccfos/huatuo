// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package agent

import (
	"testing"
	"time"
)

func TestParsePrometheusText(t *testing.T) {
	input := `# HELP huatuo_cpu_usage CPU usage
# TYPE huatuo_cpu_usage gauge
huatuo_cpu_usage{cpu="0",mode="user"} 12.5
huatuo_net_drop_total{device="eth0"} 3
other_metric 7
`
	samples, err := ParsePrometheusText(input)
	if err != nil {
		t.Fatalf("ParsePrometheusText() error = %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("len(samples) = %d, want 3", len(samples))
	}
	if samples[0].Name != "huatuo_cpu_usage" || samples[0].Labels["cpu"] != "0" || samples[0].Value != 12.5 {
		t.Fatalf("unexpected first sample: %#v", samples[0])
	}
}

func TestBuildSummaryFiltersPrefixesAndCategorizes(t *testing.T) {
	input := `huatuo_cpu_usage 12
huatuo_mem_oom_total 2
node_unrelated 99
`
	summary, err := BuildSummary("fixture", input, SummaryOptions{
		Prefixes: []string{"huatuo_"},
		TopN:     1,
		Now: func() time.Time {
			return time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("BuildSummary() error = %v", err)
	}
	if summary.TotalSamples != 3 || summary.MatchedSamples != 2 {
		t.Fatalf("sample counts = (%d, %d), want (3, 2)", summary.TotalSamples, summary.MatchedSamples)
	}
	if summary.Categories["cpu"] != 1 || summary.Categories["memory"] != 1 {
		t.Fatalf("unexpected categories: %#v", summary.Categories)
	}
	if len(summary.Top) != 1 || summary.Top[0].Name != "huatuo_cpu_usage" {
		t.Fatalf("unexpected top samples: %#v", summary.Top)
	}
	if summary.GeneratedAt != "2026-07-05T00:00:00Z" {
		t.Fatalf("GeneratedAt = %q", summary.GeneratedAt)
	}
}

func TestParsePrometheusTextInvalidValue(t *testing.T) {
	_, err := ParsePrometheusText("huatuo_cpu_usage not-a-number\n")
	if err == nil {
		t.Fatal("ParsePrometheusText() error = nil, want error")
	}
}

func TestParseEscapedLabels(t *testing.T) {
	samples, err := ParsePrometheusText(`huatuo_label_test{path="/tmp/a\\b",quote="a\"b"} 1`)
	if err != nil {
		t.Fatalf("ParsePrometheusText() error = %v", err)
	}
	if samples[0].Labels["path"] != `/tmp/a\b` || samples[0].Labels["quote"] != `a"b` {
		t.Fatalf("unexpected labels: %#v", samples[0].Labels)
	}
}

func TestParsePrometheusTextSkipsNonFiniteValues(t *testing.T) {
	samples, err := ParsePrometheusText("huatuo_nan NaN\nhuatuo_inf +Inf\nhuatuo_cpu_usage 1\n")
	if err != nil {
		t.Fatalf("ParsePrometheusText() error = %v", err)
	}
	if len(samples) != 1 || samples[0].Name != "huatuo_cpu_usage" {
		t.Fatalf("unexpected samples: %#v", samples)
	}
}

func TestBuildSummaryCanIncludeSamples(t *testing.T) {
	summary, err := BuildSummary("fixture", "huatuo_cpu_usage 1\n", SummaryOptions{
		Prefixes:       []string{"huatuo_"},
		IncludeSamples: true,
	})
	if err != nil {
		t.Fatalf("BuildSummary() error = %v", err)
	}
	if len(summary.Samples) != 1 || summary.Samples[0].Name != "huatuo_cpu_usage" {
		t.Fatalf("unexpected samples: %#v", summary.Samples)
	}
}

func TestParsePrometheusTextSkipsCommentsWithBOM(t *testing.T) {
	samples, err := ParsePrometheusText("\ufeff# HELP huatuo_cpu_usage CPU usage\n# TYPE huatuo_cpu_usage gauge\nhuatuo_cpu_usage 1\n")
	if err != nil {
		t.Fatalf("ParsePrometheusText() error = %v", err)
	}
	if len(samples) != 1 || samples[0].Name != "huatuo_cpu_usage" {
		t.Fatalf("unexpected samples: %#v", samples)
	}
}
