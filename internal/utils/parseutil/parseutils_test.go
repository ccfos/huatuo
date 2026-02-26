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

package parseutil

import (
	"math"
	"os"
	"strconv"
	"testing"
)

func TestReadUint(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    uint64
		wantErr bool
	}{
		{"max", strconv.FormatUint(math.MaxUint64, 10) + "\n", math.MaxUint64, false},
		{"trimmed", " 2026 ", 2026, false},
		{"invalid", "huatuo", 0, true},
		{"empty", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempFile(t, tt.content)
			got, err := ReadUint(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadUint() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReadUint() = %v, want %v", got, tt.want)
			}
		})
	}
	t.Run("non-existent", func(t *testing.T) {
		_, err := ReadUint("/non/existent")
		if err == nil {
			t.Errorf("ReadUint() expected error, got nil")
		}
	})
}

func TestReadInt(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int64
		wantErr bool
	}{
		{"min negative", strconv.FormatInt(math.MinInt64, 10) + "\n", math.MinInt64, false},
		{"valid negative", "-2026\n", -2026, false},
		{"trimmed", " 2026 ", 2026, false},
		{"invalid", "huatuo", 0, true},
		{"empty", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempFile(t, tt.content)
			got, err := ReadInt(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadInt() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReadInt() = %v, want %v", got, tt.want)
			}
		})
	}
	t.Run("non-existent", func(t *testing.T) {
		_, err := ReadInt("/non/existent")
		if err == nil {
			t.Errorf("ReadInt() expected error, got nil")
		}
	})
}

func TestRawKV(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]uint64
		wantErr bool
	}{
		{
			"valid multiple",
			"key1 2026\nkey2 " + strconv.FormatUint(math.MaxUint64, 10) + "\n",
			map[string]uint64{"key1": 2026, "key2": math.MaxUint64},
			false,
		},
		{
			"invalid format",
			"key1 2026\ninvalid\n",
			nil,
			true,
		},
		{
			"invalid value",
			"key huatuo\n",
			nil,
			true,
		},
		{
			"empty",
			"",
			map[string]uint64{},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempFile(t, tt.content)
			got, err := RawKV(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("RawKV() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !mapsEqual(got, tt.want) {
				t.Errorf("RawKV() = %v, want %v", got, tt.want)
			}
		})
	}
	t.Run("non-existent", func(t *testing.T) {
		_, err := RawKV("/non/existent")
		if err == nil {
			t.Errorf("RawKV() expected error, got nil")
		}
	})
}

func TestKV(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantKey string
		wantVal uint64
		wantErr bool
	}{
		{
			"valid",
			"key " + strconv.FormatUint(math.MaxUint64, 10) + "\n",
			"key",
			math.MaxUint64,
			false,
		},
		{
			"invalid format",
			"key\n",
			"",
			0,
			true,
		},
		{
			"invalid value",
			"key huatuo\n",
			"",
			0,
			true,
		},
		{
			"empty",
			"",
			"",
			0,
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := createTempFile(t, tt.content)
			gotKey, gotVal, err := KV(path)
			if (err != nil) != tt.wantErr {
				t.Errorf("KV() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotKey != tt.wantKey || gotVal != tt.wantVal {
				t.Errorf("KV() = %v, %v, want %v, %v", gotKey, gotVal, tt.wantKey, tt.wantVal)
			}
		})
	}
	t.Run("non-existent", func(t *testing.T) {
		_, _, err := KV("/non/existent")
		if err == nil {
			t.Errorf("KV() expected error, got nil")
		}
	})
}

// Helper to create temp file with content
func createTempFile(t *testing.T, content string) string {
	t.Helper()
	file, err := os.CreateTemp("", "parseutil_test_*")
	if err != nil {
		t.Errorf("create temp directory: %v", err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Errorf("write file: %v", err)
	}
	return file.Name()
}

// Helper to compare maps
func mapsEqual(a, b map[string]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
