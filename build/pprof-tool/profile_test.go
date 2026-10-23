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

package main

import (
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/pprof/profile"
)

func TestFetchReportsHTTPStatusBeforeBodyLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("diagnostic", 1024), http.StatusBadGateway)
	}))
	defer server.Close()

	var dst bytes.Buffer
	err := fetch(t.Context(), server.Client(), server.URL, &dst, 4)
	if err == nil || !strings.Contains(err.Error(), "HTTP status 502") {
		t.Fatalf("fetch() error = %v, want HTTP status 502", err)
	}
	if dst.Len() != 0 {
		t.Fatalf("fetch() wrote %d error bytes, want none", dst.Len())
	}
}

func TestFetchAcceptsResponseAtLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("1234"))
	}))
	defer server.Close()

	var dst bytes.Buffer
	if err := fetch(t.Context(), server.Client(), server.URL, &dst, 4); err != nil {
		t.Fatalf("fetch() error = %v", err)
	}
	if got := dst.String(); got != "1234" {
		t.Fatalf("fetch() body = %q, want %q", got, "1234")
	}
}

func TestAddProfileCPU(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sampleTypes  []*profile.ValueType
		samples      []*profile.Sample
		wantTotals   []sampleTotal
		wantPercent  float64
		wantWarnings []string
	}{
		{
			name: "sum reordered columns using actual duration",
			sampleTypes: []*profile.ValueType{
				{
					Type: "cpu",
					Unit: "nanoseconds",
				},
				{
					Type: "samples",
					Unit: "count",
				},
			},
			samples: []*profile.Sample{
				{Value: []int64{500_000_000, 50}},
				{Value: []int64{750_000_000, 75}},
			},
			wantTotals: []sampleTotal{
				{
					Type:  "cpu",
					Unit:  "nanoseconds",
					Total: 1_250_000_000,
				},
				{
					Type:  "samples",
					Unit:  "count",
					Total: 125,
				},
			},
			wantPercent: 50,
		},
		{
			name: "multiple cores can exceed one hundred percent",
			sampleTypes: []*profile.ValueType{
				{
					Type: "samples",
					Unit: "count",
				},
				{
					Type: "cpu",
					Unit: "nanoseconds",
				},
			},
			samples: []*profile.Sample{
				{Value: []int64{500, 5_000_000_000}},
			},
			wantTotals: []sampleTotal{
				{
					Type:  "samples",
					Unit:  "count",
					Total: 500,
				},
				{
					Type:  "cpu",
					Unit:  "nanoseconds",
					Total: 5_000_000_000,
				},
			},
			wantPercent: 200,
		},
		{
			name: "empty samples are valid but do not prove zero usage",
			sampleTypes: []*profile.ValueType{
				{
					Type: "cpu",
					Unit: "nanoseconds",
				},
			},
			wantTotals: []sampleTotal{
				{
					Type: "cpu",
					Unit: "nanoseconds",
				},
			},
			wantWarnings: []string{"CPU profile has no samples; this does not establish zero CPU usage"},
		},
	}

	for i := range tests {
		tt := &tests[i]
		t.Run(tt.name, func(t *testing.T) {
			p := &profile.Profile{
				SampleType:    tt.sampleTypes,
				Sample:        tt.samples,
				DurationNanos: 2_500_000_000,
			}
			totals, err := sumSampleValues(p)
			if err != nil {
				t.Fatalf("sumSampleValues() error = %v", err)
			}

			// The profile's actual duration, not the requested five seconds, is authoritative.
			endpoint := &url.URL{
				Path:     "profile",
				RawQuery: "seconds=5",
			}
			var got resourceSummary
			if err := got.addProfile(
				endpoint,
				endpoint.String(),
				p,
				totals,
			); err != nil {
				t.Fatalf("addProfile() error = %v", err)
			}

			want := resourceSummary{
				CPUPercent: &tt.wantPercent,
				Profiles: []profileSummary{{
					Endpoint:      "profile?seconds=5",
					Mode:          "cpu",
					DurationNanos: 2_500_000_000,
					Samples:       tt.wantTotals,
				}},
				Warnings: tt.wantWarnings,
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("addProfile() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAddProfileSnapshotsAndDeltas(t *testing.T) {
	t.Parallel()

	heapBytes, goroutines := int64(4096), int64(8)
	tests := []struct {
		name           string
		endpoint       url.URL
		total          sampleTotal
		wantMode       string
		wantHeapBytes  *int64
		wantGoroutines *int64
	}{
		{
			name:     "heap snapshot",
			endpoint: url.URL{Path: "heap"},
			total: sampleTotal{
				Type:  "inuse_space",
				Unit:  "bytes",
				Total: 4096,
			},
			wantMode:      "snapshot",
			wantHeapBytes: &heapBytes,
		},
		{
			name:     "goroutine snapshot",
			endpoint: url.URL{Path: "goroutine"},
			total: sampleTotal{
				Type:  "goroutine",
				Unit:  "count",
				Total: 8,
			},
			wantMode:       "snapshot",
			wantGoroutines: &goroutines,
		},
		{
			name: "heap delta is not a snapshot budget",
			endpoint: url.URL{
				Path:     "heap",
				RawQuery: "seconds=1",
			},
			total: sampleTotal{
				Type:  "inuse_space",
				Unit:  "bytes",
				Total: -4096,
			},
			wantMode: "delta",
		},
		{
			name: "goroutine delta is not a snapshot budget",
			endpoint: url.URL{
				Path:     "goroutine",
				RawQuery: "seconds=1",
			},
			total: sampleTotal{
				Type:  "goroutine",
				Unit:  "count",
				Total: -2,
			},
			wantMode: "delta",
		},
	}

	for i := range tests {
		tt := &tests[i]
		t.Run(tt.name, func(t *testing.T) {
			totals := []sampleTotal{tt.total}
			var got resourceSummary
			if err := got.addProfile(
				&tt.endpoint,
				tt.endpoint.String(),
				&profile.Profile{},
				totals,
			); err != nil {
				t.Fatalf("addProfile(%q) error = %v", tt.endpoint.String(), err)
			}

			want := resourceSummary{
				HeapInuseBytes: tt.wantHeapBytes,
				Goroutines:     tt.wantGoroutines,
				Profiles: []profileSummary{{
					Endpoint: tt.endpoint.String(),
					Mode:     tt.wantMode,
					Samples:  totals,
				}},
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("addProfile() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAddProfileRejectsInvalidResources(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint string
		duration int64
		total    sampleTotal
	}{
		{
			name:     "zero cpu duration",
			endpoint: "profile",
			total: sampleTotal{
				Type:  "cpu",
				Unit:  "nanoseconds",
				Total: 1,
			},
		},
		{
			name:     "negative cpu duration",
			endpoint: "profile",
			duration: -1,
			total: sampleTotal{
				Type:  "cpu",
				Unit:  "nanoseconds",
				Total: 1,
			},
		},
		{
			name:     "missing cpu metric",
			endpoint: "profile",
			duration: 1,
			total: sampleTotal{
				Type:  "samples",
				Unit:  "count",
				Total: 1,
			},
		},
		{
			name:     "heap unit must be bytes",
			endpoint: "heap",
			total: sampleTotal{
				Type:  "inuse_space",
				Unit:  "kilobytes",
				Total: 1,
			},
		},
		{
			name:     "negative snapshot count",
			endpoint: "goroutine",
			total: sampleTotal{
				Type:  "goroutine",
				Unit:  "count",
				Total: -1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result resourceSummary
			err := result.addProfile(
				&url.URL{Path: tt.endpoint},
				tt.endpoint,
				&profile.Profile{DurationNanos: tt.duration},
				[]sampleTotal{tt.total},
			)
			if err == nil {
				t.Errorf(
					"addProfile(%q, duration=%d, total=%+v) succeeded, want error",
					tt.endpoint,
					tt.duration,
					tt.total,
				)
			}
		})
	}
}

func TestSumSampleValuesSignedBoundaries(t *testing.T) {
	t.Parallel()

	p := &profile.Profile{
		SampleType: []*profile.ValueType{
			{
				Type: "positive",
				Unit: "count",
			},
			{
				Type: "negative",
				Unit: "count",
			},
			{
				Type: "delta",
				Unit: "bytes",
			},
		},
		Sample: []*profile.Sample{
			{Value: []int64{math.MaxInt64 - 1, math.MinInt64 + 1, 10}},
			{Value: []int64{1, -1, -20}},
		},
	}
	got, err := sumSampleValues(p)
	if err != nil {
		t.Fatalf("sumSampleValues() error = %v", err)
	}

	want := []sampleTotal{
		{
			Type:  "positive",
			Unit:  "count",
			Total: math.MaxInt64,
		},
		{
			Type:  "negative",
			Unit:  "count",
			Total: math.MinInt64,
		},
		{
			Type:  "delta",
			Unit:  "bytes",
			Total: -10,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("sumSampleValues() mismatch (-want +got):\n%s", diff)
	}
}

func TestSumSampleValuesRejectsInvalidProfiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		types   []*profile.ValueType
		samples []*profile.Sample
	}{
		{name: "missing sample types"},
		{
			name:  "missing type name",
			types: []*profile.ValueType{{Unit: "count"}},
		},
		{
			name:  "missing unit",
			types: []*profile.ValueType{{Type: "samples"}},
		},
		{
			name: "duplicate type and unit",
			types: []*profile.ValueType{
				{
					Type: "samples",
					Unit: "count",
				},
				{
					Type: "samples",
					Unit: "count",
				},
			},
		},
		{
			name: "positive overflow",
			types: []*profile.ValueType{{
				Type: "samples",
				Unit: "count",
			}},
			samples: []*profile.Sample{
				{Value: []int64{math.MaxInt64}},
				{Value: []int64{1}},
			},
		},
		{
			name: "negative overflow",
			types: []*profile.ValueType{{
				Type: "samples",
				Unit: "count",
			}},
			samples: []*profile.Sample{
				{Value: []int64{math.MinInt64}},
				{Value: []int64{-1}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &profile.Profile{
				SampleType: tt.types,
				Sample:     tt.samples,
			}
			if got, err := sumSampleValues(p); err == nil {
				t.Errorf("sumSampleValues() = %+v, want error", got)
			}
		})
	}
}
