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

package profile_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/profile"
)

func TestParseProcessFrames(t *testing.T) {
	tests := []struct {
		name      string
		parse     func(context.Context, *profile.ParseInput) (*profile.Data, error)
		data      string
		names     map[int]string
		want      []string
		wantError string
	}{
		{
			name:  "java multi",
			parse: profile.ParseCollapsedData,
			data:  `[{"pid":123,"output":"main;work 7\n"}]`,
			names: map[int]string{123: "Main"},
			want:  []string{"process 123:Main", "main", "work"},
		},
		{
			name:  "java empty resolved name",
			parse: profile.ParseCollapsedData,
			data:  `[{"pid":123,"output":"main;work 7\n"}]`,
			names: map[int]string{123: ""},
			want:  []string{"process 123:", "main", "work"},
		},
		{
			name:      "java missing name",
			parse:     profile.ParseCollapsedData,
			data:      `[{"pid":123,"output":"main;work 7\n"}]`,
			wantError: "missing process name for PID 123",
		},
		{
			name:  "java empty record needs no name",
			parse: profile.ParseCollapsedData,
			data:  `[{"pid":123,"output":""}]`,
		},
		{
			name:  "python multi needs no name",
			parse: profile.ParseRawData,
			data:  `[{"pid":123,"output":"process 123;main;work 7\n"}]`,
			want:  []string{"process 123", "main", "work"},
		},
		{
			name:  "java single",
			parse: profile.ParseCollapsedData,
			data:  "main;work 7\n",
			names: map[int]string{123: "Main"},
			want:  []string{"Main#123", "main", "work"},
		},
		{
			name:  "python single",
			parse: profile.ParseRawData,
			data:  "main;work 7\n",
			names: map[int]string{123: "app.py"},
			want:  []string{"app.py#123", "main", "work"},
		},
		{
			name:      "single missing name",
			parse:     profile.ParseRawData,
			data:      "main;work 7\n",
			wantError: "missing process name for PID 123",
		},
		{
			name:  "invalid lines remain ignored",
			parse: profile.ParseRawData,
			data:  "invalid\nmain;work no-count\nmain;work 7\n",
			names: map[int]string{123: "app.py"},
			want:  []string{"app.py#123", "main", "work"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.parse(t.Context(), &profile.ParseInput{
				PID:          123,
				Data:         []byte(tt.data),
				ProcessNames: tt.names,
				ProfileType:  profile.ProfileTypeCpuSample,
				Opt:          &profile.ParseOption{SampleRate: 100},
			})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("parse error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			window := profile.NewWindow(data, "container")
			if window.Profile.Period != 10000000 || window.Profile.TimeNanos != 0 || window.Profile.DurationNanos != 0 {
				t.Fatalf("unexpected sampling/window metadata: %v", window.Profile)
			}
			var frames []string
			var total int64
			err = formatter.WalkProfile(t.Context(), window.Profile, func(stack []string, value int64) error {
				frames = append(frames, stack...)
				total += value
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(frames, tt.want) {
				t.Fatalf("frames = %q, want %q", frames, tt.want)
			}
			if tt.want != nil && total != 70000000 {
				t.Fatalf("sample value = %d, want 70000000", total)
			}
		})
	}
}

func TestParseCancellationPrecedesMetadataValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	parsers := map[string]func(context.Context, *profile.ParseInput) (*profile.Data, error){
		"collapsed": profile.ParseCollapsedData,
		"raw":       profile.ParseRawData,
	}
	for name, parse := range parsers {
		t.Run(name, func(t *testing.T) {
			_, err := parse(ctx, &profile.ParseInput{PID: 123, Data: []byte("main 1\n")})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want cancellation", err)
			}
		})
	}
}
