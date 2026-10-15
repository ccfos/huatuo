//go:build linux

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
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/prometheus/procfs"
)

func TestParseOptions(t *testing.T) {
	if _, err := parseOptions([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v, want flag.ErrHelp", err)
	}
	for _, tc := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{name: "valid", args: []string{"--pid", "123", "--output-dir", "/tmp/capture"}, valid: true},
		{name: "missing pid", args: []string{"--output-dir", "/tmp/capture"}},
		{name: "zero pid", args: []string{"--pid", "0", "--output-dir", "/tmp/capture"}},
		{name: "negative pid", args: []string{"--pid", "-1", "--output-dir", "/tmp/capture"}},
		{name: "relative directory", args: []string{"--pid", "123", "--output-dir", "capture"}},
		{name: "unknown option", args: []string{"--pid", "123", "--interval-ms", "100"}},
		{name: "extra positional argument", args: []string{"--pid", "123", "--output-dir", "/tmp/capture", "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseOptions(tc.args)
			if (err == nil) != tc.valid {
				t.Fatalf("parseOptions(%v) error = %v, want valid=%t", tc.args, err, tc.valid)
			}
		})
	}
}

func sampleAt(ms int, cpu float64, rss, fds int64) *sample {
	when := time.Unix(100, 0).Add(time.Duration(ms) * time.Millisecond)
	return &sample{
		pid: 123, startTicks: 456, cpuAt: when, ioAt: when,
		readSpan: time.Millisecond, cpuSeconds: cpu, rssBytes: rss, fds: fds,
		io: procfs.ProcIO{ReadBytes: uint64(ms), WriteBytes: uint64(ms) * 2, RChar: uint64(ms) * 3, WChar: uint64(ms) * 4},
	}
}

func TestAggregate(t *testing.T) {
	// Keep short sample literals on one line to make the trajectories comparable.
	for _, tc := range []struct {
		name    string
		samples []*sample
		mean    float64
		peak    float64
		windows int
	}{
		{
			name: "weighted mean",
			samples: []*sample{
				sampleAt(0, 0, 10, 3), sampleAt(100, 0.1, 20, 4), sampleAt(300, 0.1, 15, 3),
			},
			mean: 100.0 / 3, peak: 100, windows: 2,
		},
		{
			name: "short final tick",
			samples: []*sample{
				sampleAt(0, 0, 10, 3), sampleAt(100, 0.02, 20, 4), sampleAt(101, 0.03, 30, 5),
			},
			mean: 0.03 / 0.101 * 100, peak: 20, windows: 1,
		},
		{
			name: "accumulate short intervals",
			samples: []*sample{
				sampleAt(0, 0, 10, 3), sampleAt(100, 0.02, 20, 4),
				sampleAt(150, 0.06, 15, 3), sampleAt(200, 0.1, 10, 3),
			},
			mean: 50, peak: 80, windows: 2,
		},
		{
			name: "multiple cores",
			samples: []*sample{
				sampleAt(0, 0, 30, 5), sampleAt(100, 0.2, 20, 4), sampleAt(200, 0.4, 10, 3),
			},
			mean: 200, peak: 200, windows: 2,
		},
		{
			name: "no full peak window", samples: []*sample{sampleAt(0, 0, 10, 3), sampleAt(99, 0.01, 20, 4)},
			mean: 0.01 / 0.099 * 100, windows: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var summary aggregate
			for _, s := range tc.samples {
				if err := summary.add(s); err != nil {
					t.Fatal(err)
				}
			}
			summary.periodicSamples = len(tc.samples) - 2
			result := summary.result(123, nil)
			if math.Abs(result.CPUPercent.Mean-tc.mean) > 1e-9 || result.CPUPeakWindowCount != tc.windows {
				t.Fatalf("CPU mean/windows = %v/%d, want %v/%d",
					result.CPUPercent.Mean,
					result.CPUPeakWindowCount,
					tc.mean,
					tc.windows,
				)
			}
			if tc.windows == 0 {
				hasPeak := result.CPUPercent.Peak != nil || result.CPUPeakWindowSeconds != nil
				if hasPeak || result.Quality.UsableForPeak {
					t.Fatalf("short window reported a usable peak: %+v", result)
				}
			} else if result.CPUPercent.Peak == nil || math.Abs(*result.CPUPercent.Peak-tc.peak) > 1e-9 {
				t.Fatalf("CPU peak = %v, want %v", result.CPUPercent.Peak, tc.peak)
			}
			first, last := tc.samples[0], tc.samples[len(tc.samples)-1]
			wantRSS := gauge{Before: first.rssBytes, After: last.rssBytes, Delta: last.rssBytes - first.rssBytes}
			wantFDs := gauge{Before: first.fds, After: last.fds, Delta: last.fds - first.fds}
			for _, s := range tc.samples {
				wantRSS.Peak = max(wantRSS.Peak, s.rssBytes)
				wantFDs.Peak = max(wantFDs.Peak, s.fds)
			}
			if *result.RSSBytes != wantRSS || *result.FDs != wantFDs {
				t.Fatalf("RSS/FD gauges = %+v/%+v, want %+v/%+v",
					result.RSSBytes,
					result.FDs,
					wantRSS,
					wantFDs,
				)
			}
			wantReadDelta := last.io.ReadBytes - first.io.ReadBytes
			if result.IO.ReadBytes.Delta != wantReadDelta || math.Abs(result.IO.WChar.PerSecond-4000) > 1e-9 {
				t.Fatalf("I/O summary = %+v, want matching deltas and actual-window rates", result.IO)
			}
		})
	}
}

func TestAggregateRejectsInvalidSamples(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sample)
	}{
		{name: "pid reuse", change: func(s *sample) { s.startTicks++ }},
		{name: "different pid", change: func(s *sample) { s.pid++ }},
		{name: "zero cpu interval", change: func(s *sample) { s.cpuAt = sampleAt(100, 0, 0, 0).cpuAt }},
		{name: "backward io time", change: func(s *sample) { s.ioAt = sampleAt(99, 0, 0, 0).ioAt }},
		{name: "cpu counter decreased", change: func(s *sample) { s.cpuSeconds = 0 }},
		{name: "read bytes decreased", change: func(s *sample) { s.io.ReadBytes = 0 }},
		{name: "write bytes decreased", change: func(s *sample) { s.io.WriteBytes = 0 }},
		{name: "rchar decreased", change: func(s *sample) { s.io.RChar = 0 }},
		{name: "wchar decreased", change: func(s *sample) { s.io.WChar = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var summary aggregate
			if err := summary.add(sampleAt(100, 1, 10, 3)); err != nil {
				t.Fatal(err)
			}
			before := summary
			next := sampleAt(200, 2, 20, 4)
			tc.change(next)
			if err := summary.add(next); err == nil {
				t.Fatalf("add(%+v) succeeded, want rejection", next)
			}
			if !reflect.DeepEqual(summary, before) {
				t.Fatal("rejected sample changed the summary")
			}
		})
	}
}

func TestAggregateQuality(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*aggregate)
		cause  error
	}{
		{name: "skipped sample", change: func(a *aggregate) { a.skippedSamples = 1 }},
		{name: "long gap", change: func(a *aggregate) { a.maxSampleGap = maxSampleGap + time.Nanosecond }},
		{name: "slow read", change: func(a *aggregate) { a.maxSampleSpan = sampleInterval + time.Nanosecond }},
		{name: "no periodic samples", change: func(a *aggregate) { a.periodicSamples = 0 }},
		{name: "no cpu peak windows", change: func(a *aggregate) { a.cpuPeakWindows = 0 }},
		{name: "aborted capture", cause: errors.New("capture aborted")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var summary aggregate
			for _, s := range []*sample{sampleAt(0, 0, 10, 3), sampleAt(100, 0.01, 10, 3), sampleAt(200, 0.02, 10, 3)} {
				if err := summary.add(s); err != nil {
					t.Fatal(err)
				}
			}
			summary.periodicSamples = 1
			if tc.change != nil {
				tc.change(&summary)
			}
			result := summary.result(123, tc.cause)
			if result.Quality.UsableForPeak || len(result.Quality.Reasons) == 0 {
				t.Fatalf("quality = %+v, want unusable with a reason", result.Quality)
			}
		})
	}
}

func TestUnavailableResourcesAndFinish(t *testing.T) {
	directory := t.TempDir()
	var summary aggregate
	if err := finish(options{pid: 123, outputDir: directory}, &summary, nil); err == nil {
		t.Fatal("finish with no samples succeeded")
	}
	data, err := os.ReadFile(filepath.Join(directory, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result captureResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	hasResources := result.RSSBytes != nil || result.IO != nil
	hasCPUTime := result.CPUSeconds != nil || result.ProcessStartTimeTicks != nil
	if hasResources || hasCPUTime {
		t.Fatalf("unavailable resources are not null: %s", data)
	}
	if result.Errors == nil || result.Quality.Reasons == nil {
		t.Fatalf("required arrays were null: %s", data)
	}
	if result.Complete || len(result.Errors) == 0 {
		t.Fatalf("empty capture was marked complete: %s", data)
	}
	if err := os.Mkdir(filepath.Join(directory, "blocked.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(directory, "blocked.json"), &result); err == nil {
		t.Fatal("publishing over a directory succeeded")
	}
	files, err := filepath.Glob(filepath.Join(directory, ".resource-*.json"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary JSON files = %v, error = %v", files, err)
	}
}
