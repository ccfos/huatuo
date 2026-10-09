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
	"strings"
	"testing"
	"time"
)

func TestValidatePerfDuration(t *testing.T) {
	const maxSeconds = int64(time.Duration(1<<63-1) / time.Second)
	tests := []struct {
		name      string
		seconds   int
		want      time.Duration
		wantError string
	}{
		{name: "zero", seconds: 0, wantError: "greater than zero"},
		{name: "negative", seconds: -1, wantError: "greater than zero"},
		{name: "valid", seconds: 5, want: 5 * time.Second},
		{
			name:      "overflow",
			seconds:   int(maxSeconds + 1),
			wantError: "exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validatePerfDuration(tt.seconds)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("validatePerfDuration() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("validatePerfDuration() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("validatePerfDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}
