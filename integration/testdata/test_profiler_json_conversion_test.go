//go:build integration

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

package integration_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestJSONConversionFailurePreservesOutput(t *testing.T) {
	const previous = "previous result\n"
	invalid := &types.AggregationWindow{ProfileType: "invalid"}

	for _, kind := range []string{"existing", "absent", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "profile.json")
			switch kind {
			case "existing":
				if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				path = dir
			}

			formatter, err := factory.NewFormatter(output.Config{
				Format:      output.FormatJSON,
				Destination: path,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := formatter.Close(); err != nil {
					t.Error(err)
				}
			})

			if err := formatter.Add(t.Context(), invalid); err == nil {
				t.Fatal("Add() error = nil, want conversion failure")
			}

			switch kind {
			case "existing":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != previous {
					t.Fatalf("previous result = %q, want %q", data, previous)
				}
			case "absent":
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("conversion failure left output file: %v", err)
				}
			case "directory":
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("conversion failure created %d output files", len(entries))
				}
			}
		})
	}
}
