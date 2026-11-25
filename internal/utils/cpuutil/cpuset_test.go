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
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestReadListCount(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		want         uint64
		wantErr      bool
		wantParseErr bool
	}{
		{name: "ranges", content: "0-3,8,10-11\n", want: 7},
		{name: "single", content: "7\n", want: 1},
		{
			name:    "overlap retains counts",
			content: "1,1-3\n",
			want:    4,
		},
		{
			name:    "large range without expansion",
			content: "0-18446744073709551614\n",
			want:    math.MaxUint64,
		},
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

			got, err := ReadListCount(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("ReadListCount() error = nil, want error")
				}
				var parseErr *strconv.NumError
				if tt.wantParseErr && !errors.As(err, &parseErr) {
					t.Errorf("ReadListCount() error = %v, want *strconv.NumError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadListCount() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ReadListCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReadListMaxID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{name: "sparse", content: "0,2-3\n", want: 3},
		{
			name:    "unsorted ranges",
			content: "8-10,0-3,7\n",
			want:    10,
		},
		{
			name:    "invalid earlier ID",
			content: "x,7\n",
			want:    -1,
		},
		{
			name:    "reversed range",
			content: "3-1\n",
			want:    -1,
		},
		{
			name:    "maximum int",
			content: strconv.FormatUint(uint64(math.MaxInt), 10),
			want:    math.MaxInt,
		},
		{
			name:    "ID outside int",
			content: "18446744073709551615",
			want:    -1,
		},
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

			got := ReadListMaxID(path)
			if got != tt.want {
				t.Errorf("ReadListMaxID() = %d, want %d", got, tt.want)
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

func TestNormalizeList(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		wantErr           bool
	}{
		{name: "all"},
		{
			name:  "single",
			input: "7",
			want:  "7",
		},
		{
			name:  "mixed",
			input: "1,3-10",
			want:  "1,3-10",
		},
		{
			name:  "merge duplicates",
			input: " 3,1-3,2 ",
			want:  "1-3",
		},
		{
			name:  "degenerate range",
			input: "1-1",
			want:  "1",
		},
		{
			name:  "sparse high ID",
			input: "2147483647",
			want:  "2147483647",
		},
		{
			name:    "negative",
			input:   "-1",
			wantErr: true,
		},
		{
			name:    "reversed",
			input:   "3-1",
			wantErr: true,
		},
		{
			name:    "empty component",
			input:   "1,,2",
			wantErr: true,
		},
		{
			name:    "trailing comma",
			input:   "1,",
			wantErr: true,
		},
		{
			name:    "internal whitespace",
			input:   "1, 2",
			wantErr: true,
		},
		{
			name:    "range whitespace",
			input:   "1 -2",
			wantErr: true,
		},
		{
			name:    "sign",
			input:   "+1",
			wantErr: true,
		},
		{
			name:    "nondigit",
			input:   "a",
			wantErr: true,
		},
		{
			name:    "overflow",
			input:   "2147483648",
			wantErr: true,
		},
		{
			name:  "merged overlap",
			input: "5-7,1-2,2-6",
			want:  "1-7",
		},
		{
			name:  "expansion boundary",
			input: "0-65535",
			want:  "0-65535",
		},
		{
			name:    "overlap does not bypass expansion bound",
			input:   "0-65535,0",
			wantErr: true,
		},
		{
			name:    "expansion bound",
			input:   "0-2147483647",
			wantErr: true,
		},
		{
			name:    "input bound",
			input:   strings.Repeat("1", MaxListLength+1),
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeList(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected invalid CPU list")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeList() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	ids, err := ParseList("3,1-3")
	if err != nil || !slices.Equal(ids, []int{1, 2, 3}) {
		t.Fatalf("ParseList() = %v, %v", ids, err)
	}
}

func TestValidateOnline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "online")
	if err := os.WriteFile(path, []byte("20,3-4,0,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		ids     []int
		wantErr bool
	}{
		{name: "all"},
		{
			name: "sparse",
			ids:  []int{3, 20},
		},
		{
			name: "unsorted selection",
			ids:  []int{20, 0, 3},
		},
		{
			name:    "negative selection",
			ids:     []int{-1},
			wantErr: true,
		},
		{
			name:    "offline gap",
			ids:     []int{1},
			wantErr: true,
		},
		{
			name:    "missing",
			ids:     []int{21},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOnline(tc.ids, path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateOnline() = %v", err)
			}
		})
	}
	if err := ValidateOnline([]int{0}, path+"-missing"); err == nil {
		t.Fatal("missing online file accepted")
	}
	if err := os.WriteFile(path, []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOnline([]int{0}, path); err == nil {
		t.Fatal("invalid online file accepted")
	}
}

func BenchmarkCPUList(b *testing.B) {
	for _, tc := range []struct{ name, list string }{
		{
			name: "dense",
			list: "0-191",
		},
		{
			name: "sparse",
			list: "0,2-3,8,10-11",
		},
		{
			name: "large",
			list: "0-65535",
		},
	} {
		b.Run(tc.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "online")
			if err := os.WriteFile(path, []byte(tc.list), 0o600); err != nil {
				b.Fatal(err)
			}
			b.Run("Count", func(b *testing.B) {
				for b.Loop() {
					if _, err := ReadListCount(path); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Max", func(b *testing.B) {
				for b.Loop() {
					if ReadListMaxID(path) < 0 {
						b.Fatal("missing maximum CPU ID")
					}
				}
			})
			b.Run("Parse", func(b *testing.B) {
				for b.Loop() {
					if _, err := ParseList(tc.list); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Normalize", func(b *testing.B) {
				for b.Loop() {
					if _, err := NormalizeList(tc.list); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Online", func(b *testing.B) {
				for b.Loop() {
					if err := ValidateOnline([]int{0}, path); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
