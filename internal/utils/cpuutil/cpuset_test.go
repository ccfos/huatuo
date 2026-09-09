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

package cpuutil

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseOnlineCores(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		want         uint64
		wantErr      bool
		wantParseErr bool
	}{
		{name: "ranges", content: "0-3,8,10-11\n", want: 7},
		{name: "single", content: "7\n", want: 1},
		{name: "invalid range", content: "3-1\n", wantErr: true},
		{name: "invalid range end", content: "1-x\n", wantErr: true, wantParseErr: true},
		{name: "count overflow", content: "7,0-18446744073709551615\n", wantErr: true},
		{name: "accumulated count overflow", content: "0-18446744073709551614,7\n", wantErr: true},
		{name: "empty", content: "\n"},
		{name: "whitespace", content: " \t\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cpuset")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			got, err := ParseOnlineCores(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ParseOnlineCores() error = nil, want error")
				}
				var parseErr *strconv.NumError
				if tt.wantParseErr && !errors.As(err, &parseErr) {
					t.Errorf("ParseOnlineCores() error = %v, want *strconv.NumError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseOnlineCores() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ParseOnlineCores() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMaxOnlineCPU(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{name: "sparse", content: "0,2-3\n", want: 3},
		{name: "single", content: "7\n", want: 7},
		{name: "empty", content: "\n", want: -1},
		{name: "invalid last ID", content: "0,x\n", want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "online")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			got := MaxOnlineCPU(path)
			if got != tt.want {
				t.Errorf("MaxOnlineCPU() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBoundCoresFallback(t *testing.T) {
	got, err := BoundCores(math.MaxUint64, 0, 0, 4)
	if err != nil {
		t.Fatalf("BoundCores() error = %v", err)
	}
	if got != 4 {
		t.Errorf("BoundCores() = %v, want 4", got)
	}
}

func TestCPUListIntersectionCount(t *testing.T) {
	tests := []struct {
		name    string
		lists   []string
		want    uint64
		wantErr bool
	}{
		{name: "no lists"},
		{name: "offline members", lists: []string{"0-3", "2-5"}, want: 2},
		{name: "disjoint", lists: []string{"0-3", "4-7"}},
		{name: "sparse hierarchy", lists: []string{"0-7", "1-4,6", "2-3,6-7"}, want: 3},
		{name: "unsorted overlapping", lists: []string{"3-5,0-3,2,7", "0-7"}, want: 7},
		{name: "empty list", lists: []string{"0-3", "\n"}},
		{name: "invalid after empty intersection", lists: []string{"0", "1", "bad"}, wantErr: true},
		{name: "empty item", lists: []string{"0,,1"}, wantErr: true},
		{name: "reverse range", lists: []string{"4-2"}, wantErr: true},
		{name: "invalid end", lists: []string{"0-1-2"}, wantErr: true},
		{name: "huge range stays compact", lists: []string{"0-18446744073709551615", "7-9"}, want: 3},
		{name: "max endpoint", lists: []string{"18446744073709551614-18446744073709551615,18446744073709551615"}, want: 2},
		{name: "unrepresentable count", lists: []string{"0-18446744073709551615"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CPUListIntersectionCount(tt.lists...)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CPUListIntersectionCount() error = %v, want error %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("CPUListIntersectionCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseCPUListCount(t *testing.T) {
	got, err := ParseCPUListCount("0-3,8,10-11\n")
	if err != nil || got != 7 {
		t.Fatalf("ParseCPUListCount() = %d, %v, want 7, nil", got, err)
	}
}
