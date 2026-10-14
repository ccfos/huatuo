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

package output_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	"github.com/ccfos/huatuo/internal/profiler/output"
	_ "github.com/ccfos/huatuo/internal/profiler/output/raw"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestFormatterCombinesWindowsAndCompletesOnce(t *testing.T) {
	var destination bytes.Buffer
	formatter, err := output.NewFormatter(output.FormatCollapsed, &destination, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile := &profilev1.Profile{
		StringTable: []string{"", "work"},
		Function:    []*profilev1.Function{{Id: 1, Name: 1}},
		Location:    []*profilev1.Location{{Id: 1, Line: []*profilev1.Line{{FunctionId: 1}}}},
		Sample:      []*profilev1.Sample{{LocationId: []uint64{1}, Value: []int64{7}}},
	}
	for range 2 {
		if err := formatter.Add(t.Context(), &types.ProfilingWindow{Profile: profile}); err != nil {
			t.Fatal(err)
		}
	}
	if destination.Len() != 0 {
		t.Fatal("output finalized before Complete")
	}
	for range 2 {
		if err := formatter.Complete(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if destination.String() != "work 14\n" {
		t.Fatalf("output=%q", destination.String())
	}
	if err := formatter.Add(t.Context(), &types.ProfilingWindow{Profile: profile}); err == nil {
		t.Fatal("Add accepted after completion")
	}
}

func TestFormatterFailurePreventsCompletion(t *testing.T) {
	var destination bytes.Buffer
	formatter, err := output.NewFormatter(output.FormatCollapsed, &destination, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := formatter.Add(ctx, &types.ProfilingWindow{Profile: &profilev1.Profile{}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := formatter.Complete(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if destination.Len() != 0 {
		t.Fatal("failed formatter wrote output")
	}
}
