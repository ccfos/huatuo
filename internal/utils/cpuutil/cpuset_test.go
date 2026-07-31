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
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestCPUSetCount(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    uint64
		wantErr bool
	}{
		{name: "ranges", content: "0-3,8,10-11\n", want: 7},
		{name: "single", content: "7\n", want: 1},
		{name: "invalid range", content: "3-1\n", wantErr: true},
		{name: "empty", content: "\n", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cpuset")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}

			got, err := CPUSetCount(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("CPUSetCount() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("CPUSetCount() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("CPUSetCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCPUCapacityFallback(t *testing.T) {
	got, err := CPUCapacity(math.MaxUint64, 0, 0, 4)
	if err != nil {
		t.Fatalf("CPUCapacity() error = %v", err)
	}
	if got != 4 {
		t.Errorf("CPUCapacity() = %v, want 4", got)
	}
}
