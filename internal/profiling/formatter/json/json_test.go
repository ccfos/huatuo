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

package json

import (
	"os"
	"path/filepath"
	"testing"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestConversionFailurePreservesDestination(t *testing.T) {
	for _, kind := range []string{"existing", "absent", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "profile.json")
			const original = "previous result\n"
			if kind == "existing" {
				if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if kind == "directory" {
				path = dir
			}
			f, err := NewFormatter(output.Config{Destination: path})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := f.Add(t.Context(), &types.AggregationWindow{ProfileType: "invalid"}); err == nil {
				t.Fatal("expected profile conversion error")
			}
			switch kind {
			case "existing":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != original {
					t.Fatalf("previous result changed to %q", data)
				}
			case "absent":
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("output created after failed conversion: %v", err)
				}
			case "directory":
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("created %d result files after failed conversion", len(entries))
				}
			}
		})
	}
}
