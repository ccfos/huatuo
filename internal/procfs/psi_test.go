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

package procfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePSIFile(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantErr   bool
		wantSome  bool
		wantFull  bool
		wantAvg10 float64
		wantTotal uint64
	}{
		{
			name:      "some and full lines",
			content:   "some avg10=1.23 avg60=4.56 avg300=7.89 total=1000000\nfull avg10=0.10 avg60=0.20 avg300=0.30 total=2000000\n",
			wantSome:  true,
			wantFull:  true,
			wantAvg10: 1.23,
			wantTotal: 1000000,
		},
		{
			name:      "some line only",
			content:   "some avg10=2.00 avg60=0.00 avg300=0.00 total=42\n",
			wantSome:  true,
			wantAvg10: 2.00,
			wantTotal: 42,
		},
		{
			name:    "empty file",
			content: "",
			wantErr: true,
		},
		{
			name:    "malformed value",
			content: "some avg10=abc avg60=0.00 avg300=0.00 total=5\n",
			wantErr: true,
		},
		{
			name:      "unknown prefix ignored",
			content:   "future avg10=9.99 avg60=9.99 avg300=9.99 total=1\nsome avg10=0.50 avg60=0.00 avg300=0.00 total=7\n",
			wantSome:  true,
			wantAvg10: 0.50,
			wantTotal: 7,
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pressure")
			if err := os.WriteFile(path, []byte(tests[i].content), 0o600); err != nil {
				t.Fatalf("WriteFile() error=%v", err)
			}

			stats, err := PSIStatsFromFile(path)
			if tests[i].wantErr {
				if err == nil {
					t.Fatal("PSIStatsFromFile() error=nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("PSIStatsFromFile() error=%v", err)
			}

			if got := stats.Some != nil; got != tests[i].wantSome {
				t.Errorf("Some present=%v, want %v", got, tests[i].wantSome)
			}
			if got := stats.Full != nil; got != tests[i].wantFull {
				t.Errorf("Full present=%v, want %v", got, tests[i].wantFull)
			}
			if tests[i].wantSome {
				if stats.Some.Avg10 != tests[i].wantAvg10 {
					t.Errorf("Some.Avg10=%v, want %v", stats.Some.Avg10, tests[i].wantAvg10)
				}
				if stats.Some.Total != tests[i].wantTotal {
					t.Errorf("Some.Total=%d, want %d", stats.Some.Total, tests[i].wantTotal)
				}
			}
		})
	}
}

func TestParsePSIFileMissing(t *testing.T) {
	if _, err := PSIStatsFromFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("PSIStatsFromFile(missing) error=nil, want error")
	}
}
