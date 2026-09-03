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

package profiler

import (
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/flamegraph"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

func TestParseFlamegraphFramesBuildsCPUProfile(t *testing.T) {
	start := time.Unix(1700000000, 123).UTC()
	duration := 10 * time.Second
	profileData, err := ParseFlamegraphFrames(
		start,
		duration,
		ProfileTypeCpuSample,
		[]flamegraph.FrameData{
			{Level: 0, Value: 5, Label: "root"},
			{Level: 1, Value: 3, Self: 3, Label: "left"},
			{Level: 1, Value: 2, Self: 2, Label: "right"},
		},
		&ParseOption{SampleRate: 100},
	)
	if err != nil {
		t.Fatalf("ParseFlamegraphFrames returned error: %v", err)
	}

	profile := &profileData.Profile
	if profileData.ProfileType != ProfileTypeCpuSample {
		t.Errorf(
			"ProfileType = %q, want %q",
			profileData.ProfileType,
			ProfileTypeCpuSample,
		)
	}
	if got := profile.TimeNanos; got != start.UnixNano() {
		t.Errorf("TimeNanos = %d, want %d", got, start.UnixNano())
	}
	if got := time.Duration(profile.DurationNanos); got != duration {
		t.Errorf("DurationNanos = %s, want %s", got, duration)
	}
	if got := time.Duration(profile.Period); got != 10*time.Millisecond {
		t.Errorf("Period = %s, want %s", got, 10*time.Millisecond)
	}
	if len(profile.Sample) != 2 {
		t.Fatalf("samples = %d, want 2", len(profile.Sample))
	}

	assertValueType(
		t,
		profile.StringTable,
		profile.SampleType[0],
		"cpu",
		"nanoseconds",
	)
	assertValueType(
		t,
		profile.StringTable,
		profile.PeriodType,
		"cpu",
		"nanoseconds",
	)

	var total int64
	for _, sample := range profile.Sample {
		if len(sample.Value) != 1 {
			t.Fatalf("sample values = %v, want one value", sample.Value)
		}
		total += sample.Value[0]
	}
	if want := int64(50 * time.Millisecond); total != want {
		t.Errorf("sample total = %d, want %d", total, want)
	}
}

func TestParseFlamegraphFramesRejectsInvalidInput(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	valid := []flamegraph.FrameData{
		{Level: 0, Value: 1, Self: 1, Label: "root"},
	}
	tests := []struct {
		name     string
		start    time.Time
		duration time.Duration
		frames   []flamegraph.FrameData
		opt      *ParseOption
		wantErr  string
	}{
		{
			name:     "zero timestamp",
			duration: time.Second,
			frames:   valid,
			opt:      &ParseOption{SampleRate: 99},
			wantErr:  "start time is zero",
		},
		{
			name:    "zero duration",
			start:   start,
			frames:  valid,
			opt:     &ParseOption{SampleRate: 99},
			wantErr: "duration must be positive",
		},
		{
			name:     "missing sample rate",
			start:    start,
			duration: time.Second,
			frames:   valid,
			wantErr:  "sample rate must be positive",
		},
		{
			name:     "level gap",
			start:    start,
			duration: time.Second,
			frames: []flamegraph.FrameData{
				{Level: 0, Value: 1, Label: "root"},
				{Level: 2, Value: 1, Self: 1, Label: "leaf"},
			},
			opt:     &ParseOption{SampleRate: 99},
			wantErr: "invalid level",
		},
		{
			name:     "negative aggregate",
			start:    start,
			duration: time.Second,
			frames: []flamegraph.FrameData{
				{Level: 0, Value: -1, Label: "root"},
			},
			opt:     &ParseOption{SampleRate: 99},
			wantErr: "aggregate value must not be negative",
		},
		{
			name:     "negative self",
			start:    start,
			duration: time.Second,
			frames: []flamegraph.FrameData{
				{Level: 0, Value: 1, Self: -1, Label: "root"},
			},
			opt:     &ParseOption{SampleRate: 99},
			wantErr: "self value must not be negative",
		},
		{
			name:     "self exceeds aggregate",
			start:    start,
			duration: time.Second,
			frames: []flamegraph.FrameData{
				{Level: 0, Value: 1, Self: 2, Label: "root"},
			},
			opt:     &ParseOption{SampleRate: 99},
			wantErr: "exceeds aggregate",
		},
		{
			name:     "empty samples",
			start:    start,
			duration: time.Second,
			frames: []flamegraph.FrameData{
				{Level: 0, Value: 1, Label: "root"},
			},
			opt:     &ParseOption{SampleRate: 99},
			wantErr: "no positive self samples",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFlamegraphFrames(
				tt.start,
				tt.duration,
				ProfileTypeCpuSample,
				tt.frames,
				tt.opt,
			)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func assertValueType(
	t *testing.T,
	stringTable []string,
	valueType *ptree.ValueType,
	wantType string,
	wantUnit string,
) {
	t.Helper()
	if valueType == nil {
		t.Fatal("value type is nil")
	}
	if got := stringTable[valueType.Type]; got != wantType {
		t.Errorf("type = %q, want %q", got, wantType)
	}
	if got := stringTable[valueType.Unit]; got != wantUnit {
		t.Errorf("unit = %q, want %q", got, wantUnit)
	}
}
