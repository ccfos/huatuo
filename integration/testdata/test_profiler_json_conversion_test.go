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
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

// Cancel at the first sample conversion, after Add and BuildProfile check entry.
// This makes the output timeout race reproducible without sleeps or large inputs.
type conversionCancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *conversionCancelContext) Err() error {
	c.checks++
	if c.checks == 3 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestJSONConversionCancellationPreservesOutput(t *testing.T) {
	w := &types.AggregationWindow{
		ProfileType: profiling.ProfileTypeCpuSample,
		Aggregated: types.AggregatedProfile{
			TimeNanos: 1,
			Samples:   []types.AggregatedSample{{Value: 1}},
		},
	}
	if err := profilewindow.Validate(w); err != nil {
		t.Fatal(err)
	}
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
			f, err := factory.NewFormatter(output.Config{Format: output.FormatJSON, Destination: path})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := f.Add(&conversionCancelContext{Context: ctx, cancel: cancel}, w); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected conversion cancellation, got %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
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
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("expected no output after cancellation, got %v", err)
				}
			case "directory":
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("created %d result files after cancellation", len(entries))
				}
			}
		})
	}
}
