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

package main

import (
	"context"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf"
	flamegraphtui "github.com/ccfos/huatuo/internal/profiling/flamegraph/tui"
)

func buildTUIModel(ctx context.Context, b bpf.BPF) (*flamegraphtui.Model, error) {
	tree := flamegraphtui.NewStackTree(ctx)
	path := []string{"total"}
	if err := readAndVisitPerfStacks(b, func(frames []string, weight int64) error {
		path = append(path[:1], frames...)
		return tree.Add(path, weight)
	}); err != nil {
		return nil, fmt.Errorf("read perf stacks: %w", err)
	}

	model, err := tree.BuildModel()
	if err != nil {
		return nil, fmt.Errorf("prepare perf TUI: %w", err)
	}

	return model, nil
}
