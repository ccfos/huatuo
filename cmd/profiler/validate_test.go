// Copyright 2025 The HuaTuo Authors
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
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCPUIDList(t *testing.T) {
	numCPU := runtime.NumCPU()

	tests := []struct {
		name    string
		input   string
		want    []int
		wantErr bool
	}{
		{
			name:  "single CPU",
			input: "1",
			want:  []int{1},
		},
		{
			name:  "comma separated",
			input: "1,3,5",
			want:  []int{1, 3, 5},
		},
		{
			name:  "range",
			input: "1-3",
			want:  []int{1, 2, 3},
		},
		{
			name:  "mixed",
			input: "1,3,5-7",
			want:  []int{1, 3, 5, 6, 7},
		},
		{
			name:  "with spaces",
			input: "1, 3, 5-7",
			want:  []int{1, 3, 5, 6, 7},
		},
		{
			name:  "duplicate removal",
			input: "1,1,2-3,3",
			want:  []int{1, 2, 3},
		},
		{
			name:  "range with spaces",
			input: "1 - 3",
			want:  []int{1, 2, 3},
		},
		{
			name:    "invalid range",
			input:   "3-1",
			wantErr: true,
		},
		{
			name:    "out of range",
			input:   "999",
			wantErr: true,
		},
		{
			name:    "negative",
			input:   "-1",
			wantErr: true,
		},
		{
			name:    "invalid format",
			input:   "a,b",
			wantErr: true,
		},
		{
			name:    "empty after trim",
			input:   "  ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCPUIDList(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("out of range based on numCPU", func(t *testing.T) {
		_, err := parseCPUIDList("999")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	t.Run("valid max CPU", func(t *testing.T) {
		if numCPU > 0 {
			got, err := parseCPUIDList("0")
			require.NoError(t, err)
			assert.Equal(t, []int{0}, got)
		}
	})
}

func TestValidateMemoryMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{
			name: "virtual_alloc",
			mode: "virtual_alloc",
		},
		{
			name: "physical_alloc",
			mode: "physical_alloc",
		},
		{
			name: "physical_usage",
			mode: "physical_usage",
		},
		{
			name:    "invalid mode",
			mode:    "invalid",
			wantErr: true,
		},
		{
			name:    "empty mode",
			mode:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMemoryMode(tt.mode)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
