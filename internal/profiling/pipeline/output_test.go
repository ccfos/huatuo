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

package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

type blockingOutput struct {
	entered   chan struct{}
	release   chan struct{}
	total     int64
	completed bool
}

func (*blockingOutput) Name() output.OutputFormat { return output.FormatJSON }

func (f *blockingOutput) Add(ctx context.Context, w *types.AggregationWindow) error {
	if f.total == 0 {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	f.total += w.Aggregated.Samples[0].Value

	return nil
}
func (f *blockingOutput) Complete(context.Context) error { f.completed = true; return nil }
func (*blockingOutput) Close() error                     { return nil }

func TestPipelineCancellationInterruptsFinalOutput(t *testing.T) {
	f := &blockingOutput{entered: make(chan struct{}), release: make(chan struct{})}
	p := NewPipeline(pipelineConfig(f))
	if err := p.Enqueue(t.Context(), testBatch(1)); err != nil {
		t.Fatal(err)
	}
	p.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("final output did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() = %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt final output")
	}
	if f.total != 0 || f.completed {
		t.Fatal("canceled output completed")
	}
}
