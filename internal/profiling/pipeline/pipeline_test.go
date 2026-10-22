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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	"github.com/ccfos/huatuo/pkg/types"
)

func testBatch(value int64) *SampleBatch {
	return &SampleBatch{Samples: []aggregation.Sample{{Value: value}}}
}

func pipelineConfig(f output.Formatter) *PipelineConfig {
	return &PipelineConfig{
		AggregationConfig: AggregationConfig{ProfileType: "irqtracing:irq:count:irq:count"},
		AggregationMode:   AggregationModeOneShot,
		Formatter:         f,
	}
}

type captureOutput struct {
	name         output.OutputFormat
	windows      []*types.AggregationWindow
	completions  int
	closes       int
	err          error
	contextCheck func(context.Context)
}

func (f *captureOutput) Name() output.OutputFormat { return f.name }

func (f *captureOutput) Add(ctx context.Context, w *types.AggregationWindow) error {
	if f.contextCheck != nil {
		f.contextCheck(ctx)
	}

	f.windows = append(f.windows, w)

	return f.err
}
func (f *captureOutput) Complete(context.Context) error { f.completions++; return nil }
func (f *captureOutput) Close() error                   { f.closes++; return nil }

func TestNewPipelineConfig(t *testing.T) {
	for _, mode := range []AggregationMode{AggregationModeStreaming, AggregationModeOneShot} {
		cfg := pipelineConfig(&captureOutput{})
		cfg.AggregationMode = mode
		cfg.TracerID = "task-123"
		p := NewPipeline(cfg)
		invalidInterval := p.aggregationInterval != defaultAggregationInterval || cfg.AggregationInterval != 0
		invalidMode := p.aggregationMode != mode
		invalidTracerID := p.tracerID != cfg.TracerID
		if invalidInterval || invalidMode || invalidTracerID || cap(p.queue) != pipelineQueueCapacity {
			t.Fatal("configuration changed")
		}
	}

	if p := NewPipeline(&PipelineConfig{}); p.tracerID == "" {
		t.Fatal("missing generated tracer id")
	}
}

func TestPipelineRunDrainsAcceptedRecords(t *testing.T) {
	f := &captureOutput{}
	p := NewPipeline(pipelineConfig(f))
	for range 2 {
		if err := p.Enqueue(t.Context(), testBatch(1)); err != nil {
			t.Fatal(err)
		}
	}

	p.Close()

	if err := p.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(f.windows) != 1 || f.windows[0].Aggregated.Samples[0].Value != 2 {
		t.Fatalf("windows=%v", f.windows)
	}
}

func TestPipelineEnqueueFullQueue(t *testing.T) {
	for _, mode := range []AggregationMode{AggregationModeStreaming, AggregationModeOneShot} {
		name := map[AggregationMode]string{
			AggregationModeStreaming: "streaming",
			AggregationModeOneShot:   "one shot",
		}[mode]
		t.Run(name, func(t *testing.T) {
			f := &captureOutput{}
			cfg := pipelineConfig(f)
			cfg.AggregationMode = mode
			p := NewPipeline(cfg)
			// Freeze consumption without a production hook; this test exercises only
			// the synchronous enqueue ownership boundary.
			for range cap(p.queue) {
				if err := p.Enqueue(t.Context(), testBatch(1)); err != nil {
					t.Fatal(err)
				}
			}

			if err := p.Enqueue(t.Context(), testBatch(1)); !errors.Is(err, ErrQueueFull) {
				t.Fatalf("expected full queue error, got %v", err)
			}
		})
	}
}

func TestPipelineEnqueueRequiresActiveContext(t *testing.T) {
	p := NewPipeline(pipelineConfig(&captureOutput{}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Enqueue(ctx, testBatch(1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Enqueue() = %v, want cancellation", err)
	}
	p.Close()
	if err := p.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineReturnsFinalOutputFailure(t *testing.T) {
	failure := errors.New("store unavailable")
	f := &captureOutput{err: failure}
	p := NewPipeline(pipelineConfig(f))
	if err := p.Enqueue(t.Context(), testBatch(3)); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if err := p.Run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("Run() = %v, want output failure", err)
	}
	if len(f.windows) != 1 || f.completions != 0 {
		t.Fatalf("windows=%d completions=%d", len(f.windows), f.completions)
	}
}

func TestPipelineCancellationDoesNotExport(t *testing.T) {
	f := &captureOutput{}
	p := NewPipeline(pipelineConfig(f))
	if err := p.Enqueue(t.Context(), testBatch(1)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p.Close()
	if err := p.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(f.windows) != 0 || f.completions != 0 {
		t.Fatal("canceled pipeline exported output")
	}
}

func TestPipelineOneShotDrainsBatchAndWritesOneWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.json")
	f := testFormatter(t, output.FormatJSON, path)
	p := NewPipeline(pipelineConfig(f))
	if err := p.Enqueue(t.Context(), testBatch(9)); err != nil {
		t.Fatal(err)
	}

	p.Close()

	if err := p.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := f.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}

	var got types.ProfilingWindow
	if err := json.Unmarshal(readOutput(t, path), &got); err != nil {
		t.Fatal(err)
	}

	if got.Profile.Sample[0].Value[0] != 9 {
		t.Fatal(got.Profile)
	}
}

func TestPipelinePeriodicWindowKeepsRecordsArrivingDuringExport(t *testing.T) {
	f := &blockingOutput{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	cfg := pipelineConfig(f)
	cfg.AggregationMode = AggregationModeStreaming
	cfg.AggregationInterval = time.Millisecond
	p := NewPipeline(cfg)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	if err := p.Enqueue(t.Context(), testBatch(1)); err != nil {
		t.Fatal(err)
	}

	select {
	case <-f.entered:
	case <-time.After(time.Second):
		t.Fatal("output did not start")
	}

	if err := p.Enqueue(t.Context(), testBatch(2)); err != nil {
		t.Fatal(err)
	}

	close(f.release)
	p.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if f.total != 3 {
		t.Fatalf("total=%d", f.total)
	}
}

func TestPipelineReportsDroppedSamples(t *testing.T) {
	for _, tc := range []struct {
		name       string
		collection uint64
	}{{
		name:       "no loss",
		collection: 0,
	}, {
		name:       "collection",
		collection: 3,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &captureOutput{}
			p := NewPipeline(pipelineConfig(f))
			batch := testBatch(1)
			batch.DroppedSamples = map[string]uint64{types.DropReasonCollection: tc.collection}
			if err := p.Enqueue(t.Context(), batch); err != nil {
				t.Fatal(err)
			}

			p.Close()

			if err := p.Run(t.Context()); err != nil {
				t.Fatal(err)
			}

			got := f.windows[0].DroppedSamples
			if got[types.DropReasonCollection] != tc.collection {
				t.Fatal(got)
			}
		})
	}
}

func TestPipelineAccountsQueueFullDrops(t *testing.T) {
	f := &captureOutput{}
	p := NewPipeline(pipelineConfig(f))
	p.RecordQueueFull(3)

	p.Close()

	if err := p.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	got := f.windows[0].DroppedSamples
	if got[types.DropReasonCollection] != 3 {
		t.Fatalf("dropped samples = %v, want %d", got, 3)
	}
}

func TestPipelineFinalSnapshotUsesRuntimeContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(t.Context(), key{}, "task")
	f := &captureOutput{contextCheck: func(ctx context.Context) {
		if ctx.Err() != nil || ctx.Value(key{}) != "task" {
			t.Error("delivery lost context")
		}
	}}
	p := NewPipeline(pipelineConfig(f))
	if err := p.Enqueue(t.Context(), testBatch(1)); err != nil {
		t.Fatal(err)
	}

	p.Close()

	if err := p.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineOwnsAggregationTime(t *testing.T) {
	for _, mode := range []AggregationMode{AggregationModeStreaming, AggregationModeOneShot} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := &captureOutput{}
			cfg := pipelineConfig(f)
			cfg.AggregationMode = mode
			p := NewPipeline(cfg)
			for range 2 {
				start := time.Now()
				if err := p.consumeBatch(t.Context(), queuedBatch{
					batch:      testBatch(1),
					enqueuedAt: start,
				}); err != nil {
					t.Fatal(err)
				}

				end := start.Add(time.Second)
				if err := p.snapshot(t.Context(), end); err != nil {
					t.Fatal(err)
				}

				w := f.windows[len(f.windows)-1]
				invalidStart := w.Aggregated.TimeNanos != start.UnixNano()
				invalidDuration := w.Aggregated.DurationNanos != int64(time.Second)
				if invalidStart || invalidDuration || !p.windowStart.IsZero() {
					t.Fatal("window interval was not sealed/reset")
				}
			}
		})
	}
}

func TestPipelineLeavesFormatterCompletionToCaller(t *testing.T) {
	f := &captureOutput{}
	p := NewPipeline(pipelineConfig(f))
	if err := p.Enqueue(t.Context(), testBatch(1)); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if err := p.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.windows) != 1 || f.completions != 0 || f.closes != 0 {
		t.Fatalf("windows=%d completions=%d closes=%d", len(f.windows), f.completions, f.closes)
	}
}

func testFormatter(t *testing.T, format output.OutputFormat, destination string) output.Formatter {
	t.Helper()
	formatter, err := factory.NewFormatter(output.Config{
		Format:      format,
		Destination: destination,
		TaskID:      "task",
		ToolName:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := formatter.Close(); err != nil {
			t.Error(err)
		}
	})

	return formatter
}

func readOutput(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestPipelineCapturesConfiguration(t *testing.T) {
	cfg := &PipelineConfig{AggregationConfig: AggregationConfig{
		ContainerID: "original-container",
		ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
	}}
	p := NewPipeline(cfg)
	cfg.ContainerID = "changed"
	p.aggregator.Drain()
	if p.containerID != "original-container" {
		t.Fatal(p.containerID)
	}
}

func TestSoftIRQWindowUsesCommonProfileAndQuality(t *testing.T) {
	f := &captureOutput{}
	p := NewPipeline(&PipelineConfig{
		AggregationConfig: AggregationConfig{
			ContainerID: "irq-container",
			ProfileType: "irqtracing:irq:count:irq:count",
		},
		AggregationMode: AggregationModeOneShot,
		Formatter:       f,
	})
	if err := p.Enqueue(t.Context(), &SampleBatch{
		Samples: []aggregation.Sample{{
			UserFrames: frames("raise"),
			Labels:     []types.ProfileLabel{types.ProfileCPULabel(2), types.ProfileStringLabel("irq", "source")},
			Value:      7,
		}},
		DroppedSamples: map[string]uint64{types.DropReasonCollection: 3},
	}); err != nil {
		t.Fatal(err)
	}

	p.Close()

	if err := p.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	w := f.windows[0]
	incorrectContainer := w.ContainerID != "irq-container"
	incorrectDrops := w.DroppedSamples[types.DropReasonCollection] != 3
	incorrectValue := w.Aggregated.Samples[0].Value != 7
	if incorrectContainer || incorrectDrops || incorrectValue {
		t.Fatal(w)
	}

	if !slices.Contains(w.Aggregated.LabelSets[0], types.ProfileCPULabel(2)) {
		t.Fatal(w)
	}
}

func frames(names ...string) []types.ProfileFrame {
	result := make([]types.ProfileFrame, len(names))
	for i, name := range names {
		result[i].Name = name
	}

	return result
}

func TestPipelineReturnsAggregationFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples []aggregation.Sample
	}{
		{name: "overflow", samples: []aggregation.Sample{{Value: math.MaxInt64}, {Value: 1}, {Value: -1}}},
		{name: "invalid labels", samples: []aggregation.Sample{{Labels: []types.ProfileLabel{{Name: "invalid"}}}, {Value: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &captureOutput{}
			p := NewPipeline(pipelineConfig(f))
			if err := p.Enqueue(t.Context(), &SampleBatch{Samples: tc.samples}); err != nil {
				t.Fatal(err)
			}
			p.Close()
			if err := p.Run(t.Context()); err == nil {
				t.Fatal("Run succeeded after aggregation failure")
			}
			if len(f.windows) != 0 || f.completions != 0 {
				t.Fatal("failed pipeline exported output")
			}
		})
	}
}

func TestPipelineSnapshotTransfersDroppedSamples(t *testing.T) {
	f := &captureOutput{}
	p := NewPipeline(pipelineConfig(f))
	for _, dropped := range []uint64{3, 5} {
		start := time.Now()
		if err := p.consumeBatch(t.Context(), queuedBatch{
			batch: &SampleBatch{
				DroppedSamples: map[string]uint64{types.DropReasonCollection: dropped},
			},
			enqueuedAt: start,
		}); err != nil {
			t.Fatal(err)
		}

		if err := p.snapshot(t.Context(), start.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	if len(f.windows) != 2 || f.windows[0].DroppedSamples[types.DropReasonCollection] != 3 ||
		f.windows[1].DroppedSamples[types.DropReasonCollection] != 5 {
		t.Fatalf("windows share dropped sample state: %+v", f.windows)
	}

	if err := p.snapshot(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}

	if len(f.windows) != 2 {
		t.Fatal("empty window was exported")
	}
}

func TestPipelineSnapshotRetainsPhysicalUsage(t *testing.T) {
	for _, format := range []output.OutputFormat{output.FormatRemoteStore, output.FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			f := &captureOutput{name: format}
			cfg := pipelineConfig(f)
			cfg.ProfileType = "memory:physical_usage:bytes:space:bytes"
			p := NewPipeline(cfg)
			end := time.Unix(100, 0)
			deltas := []int64{8192, 0, -4096, -4096, -4096, 4096}
			totals := []int64{8192, 8192, 4096, 0, 0, 0}
			for i, delta := range deltas {
				if err := p.consumeBatch(t.Context(), queuedBatch{
					batch:      testBatch(delta),
					enqueuedAt: end,
				}); err != nil {
					t.Fatal(err)
				}

				end = end.Add(time.Second)
				if err := p.snapshot(t.Context(), end); err != nil {
					t.Fatal(err)
				}

				got := &f.windows[i].Aggregated
				want := delta
				if format == output.FormatRemoteStore {
					want = totals[i]
					if got.TimeNanos != end.UnixNano() || got.DurationNanos != 0 {
						t.Fatal("retained result is not timestamped at observation")
					}
				}

				if got.Samples[0].Value != want {
					t.Fatalf("window %d value=%d, want %d", i, got.Samples[0].Value, want)
				}
			}

			if f.windows[0].Aggregated.Samples[0].Value != 8192 {
				t.Fatal("later windows mutated earlier output")
			}

			if err := p.snapshot(t.Context(), end.Add(time.Second)); err != nil {
				t.Fatal(err)
			}

			wantWindows := len(deltas)
			if format == output.FormatRemoteStore {
				wantWindows++
			}

			if len(f.windows) != wantWindows {
				t.Fatalf("idle windows=%d, want %d", len(f.windows), wantWindows)
			}
		})
	}
}

func TestPipelineSnapshotEmitsInitialZeroAndRejectsLoss(t *testing.T) {
	f := &captureOutput{name: output.FormatRemoteStore}
	cfg := pipelineConfig(f)
	cfg.ProfileType = "memory:physical_usage:bytes:space:bytes"
	p := NewPipeline(cfg)
	if err := p.snapshot(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}

	if len(f.windows) != 1 || len(f.windows[0].Aggregated.Samples) != 0 {
		t.Fatal("initial zero state missing")
	}

	p.droppedSamples = map[string]uint64{types.DropReasonCollection: 1}
	if err := p.snapshot(t.Context(), time.Now()); err == nil || len(f.windows) != 1 {
		t.Fatal("collection loss published a live result")
	}
}
