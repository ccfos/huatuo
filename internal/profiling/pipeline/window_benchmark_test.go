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
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"

	"github.com/ccfos/huatuo/pkg/types"

	"github.com/ccfos/huatuo/internal/log"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
)

func benchmarkWindowAggregator(b *testing.B, size, depth int) *Pipeline {
	b.Helper()
	a := aggregationPipeline()
	for i := 0; i < size; i++ {
		frames := benchmarkStackFrames("shared", depth)
		frames[depth-1].Name = fmt.Sprintf("leaf%d", i)
		if err := a.aggregator.Add(&aggregation.Sample{
			UserFrames: frames,
			Labels:     []types.ProfileLabel{types.ProfileStringLabel("comm", "worker"), types.ProfilePIDLabel(123)},
			Value:      70000000,
		}); err != nil {
			b.Fatal(err)
		}
	}

	return a
}

// Include window preparation: each snapshot consumes its aggregation state.
// This workload does not bypass dictionary ownership to restore drained results.
func BenchmarkPipelineSnapshotCycle(b *testing.B) {
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprintf("stacks=%d/depth=32", size), func(b *testing.B) {
			f := &captureOutput{}

			start := time.Now()
			b.ReportAllocs()
			for b.Loop() {
				f.windows = f.windows[:0]
				a := benchmarkWindowAggregator(b, size, 32)
				a.formatter = f
				a.windowStart = start
				if err := a.snapshot(b.Context(), start.Add(time.Second)); err != nil {
					b.Fatal(err)
				}

				runtime.KeepAlive(f.windows[0])
			}
		})
	}
}

func BenchmarkProfilingFormatter(b *testing.B) {
	for _, format := range []output.OutputFormat{
		output.FormatCollapsed,
		output.FormatSVG,
		output.FormatSpeedscope,
		output.FormatChromeTrace,
		output.FormatJSON,
	} {
		b.Run(string(format), func(b *testing.B) {
			destination := filepath.Join(b.TempDir(), "profile.out")
			log.SetLevel("error")
			b.Cleanup(func() { log.SetLevel("info") })
			a := benchmarkWindowAggregator(b, 1000, 32)
			w := types.AggregationWindow{
				ContainerID: a.containerID,
				ProfileType: a.profileType,
				Aggregated:  a.aggregator.Drain(),
			}
			b.ReportAllocs()
			for b.Loop() {
				f, err := factory.NewFormatter(output.Config{
					Format:      format,
					Destination: destination,
				})
				if err != nil {
					b.Fatal(err)
				}

				if err = f.Add(b.Context(), &w); err != nil {
					b.Fatal(err)
				}

				if err = f.Complete(b.Context()); err != nil {
					b.Fatal(err)
				}

				if err = f.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkProfilingRemoteEncode(b *testing.B) {
	a := benchmarkWindowAggregator(b, 1000, 32)
	w := types.AggregationWindow{
		ContainerID: a.containerID,
		ProfileType: a.profileType,
		Aggregated:  a.aggregator.Drain(),
	}
	b.ReportAllocs()
	for b.Loop() {
		data, err := json.Marshal(&w)
		if err != nil {
			b.Fatal(err)
		}

		b.ReportMetric(float64(len(data)), "wire-B/op")
	}
}

// BenchmarkProfilingCycle includes the work moved out of Snapshot so a faster
// boundary cannot hide a more expensive aggregation phase.
func BenchmarkProfilingCycle(b *testing.B) {
	for _, shared := range []bool{true, false} {
		for _, format := range []output.OutputFormat{output.FormatCollapsed, output.FormatJSON} {
			b.Run(fmt.Sprintf("shared=%t/%s", shared, format), func(b *testing.B) {
				destination := filepath.Join(b.TempDir(), "profile.out")
				level := log.GetLevel()
				log.SetLevel("error")
				b.Cleanup(func() { log.SetLevel(level.String()) })
				samples := make([]aggregation.Sample, 1000)
				for i := range samples {
					prefix := fmt.Sprint(i)
					if shared {
						prefix = "shared"
					}

					frames := benchmarkStackFrames(prefix, 32)
					frames[31].Name = fmt.Sprintf("leaf%d", i)
					samples[i] = aggregation.Sample{
						UserFrames: frames,
						Labels:     []types.ProfileLabel{types.ProfileStringLabel("comm", "worker"), types.ProfilePIDLabel(123)},
						Value:      70000000,
					}
				}

				b.ReportAllocs()
				for b.Loop() {
					a := aggregationPipeline()
					for i := range samples {
						if err := a.aggregator.Add(&samples[i]); err != nil {
							b.Fatal(err)
						}
					}

					w := types.AggregationWindow{
						ContainerID: a.containerID,
						ProfileType: a.profileType,
						Aggregated:  a.aggregator.Drain(),
					}
					f, err := factory.NewFormatter(output.Config{
						Format:      format,
						Destination: destination,
					})
					if err != nil {
						b.Fatal(err)
					}

					if err = f.Add(b.Context(), &w); err != nil {
						b.Fatal(err)
					}

					if err = f.Complete(b.Context()); err != nil {
						b.Fatal(err)
					}

					if err = f.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// These workloads measure the approved batch limits, including input ownership
// copies. They are benchmarks only; no additional unit-test cases are introduced.
func BenchmarkPipelineBatchCycle(b *testing.B) {
	for _, workload := range []struct {
		name                       string
		count, depth, labels, text int
		unique                     bool
	}{
		{
			name:   "single",
			count:  1,
			depth:  32,
			labels: 2,
		},
		{
			name:   "batch256",
			count:  256,
			depth:  32,
			labels: 2,
		},
		{
			name:   "deep4096",
			count:  1,
			depth:  4096,
			labels: 2,
		},
		{
			name:   "labels64",
			count:  256,
			depth:  32,
			labels: 64,
		},
		{
			name:  "text1MiB",
			count: 1,
			depth: 1,
			text:  MaxBatchTextBytes,
		},
		{
			name:   "identities1024",
			count:  1024,
			depth:  32,
			labels: 2,
			unique: true,
		},
	} {
		b.Run(workload.name, func(b *testing.B) {
			template := aggregation.Sample{
				UserFrames: benchmarkStackFrames("user", workload.depth),
				Value:      1,
			}
			if workload.text > 0 {
				template.UserFrames[0].Name = strings.Repeat("x", workload.text)
			}

			for i := 0; i < workload.labels; i++ {
				template.Labels = append(template.Labels, types.ProfileStringLabel(fmt.Sprintf("label%02d", i), "value"))
			}

			b.ReportAllocs()
			for b.Loop() {
				sink := &benchmarkSink{}
				p := NewPipeline(&PipelineConfig{
					AggregationConfig: AggregationConfig{ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds"},
					AggregationMode:   AggregationModeOneShot,
					Formatter:         sink,
				})
				done := make(chan error, 1)
				go func() { done <- p.Run(b.Context()) }()
				var builder BatchBuilder
				for i := 0; i < workload.count; i++ {
					sample := aggregation.Sample{
						UserFrames: slices.Clone(template.UserFrames),
						Labels:     slices.Clone(template.Labels),
						Value:      1,
					}
					if workload.unique {
						sample.Labels[0].StringValue = fmt.Sprint(i)
					}

					if err := builder.Append(&sample, func(batch *SampleBatch) error {
						return p.Enqueue(b.Context(), batch)
					}); err != nil {
						b.Fatal(err)
					}
				}

				if err := builder.Flush(func(batch *SampleBatch) error { return p.Enqueue(b.Context(), batch) }); err != nil {
					b.Fatal(err)
				}

				p.Close()
				if err := <-done; err != nil {
					b.Fatal(err)
				}

				if err := sink.Complete(b.Context()); err != nil {
					b.Fatal(err)
				}

				if sink.total != int64(workload.count) {
					b.Fatalf("delivered %d of %d", sink.total, workload.count)
				}
			}
		})
	}
}

type benchmarkSink struct{ total int64 }

func (s *benchmarkSink) Add(_ context.Context, w *types.AggregationWindow) error {
	for i := range w.Aggregated.Samples {
		s.total += w.Aggregated.Samples[i].Value
	}

	return nil
}
func (*benchmarkSink) Complete(context.Context) error { return nil }
func (*benchmarkSink) Close() error                   { return nil }

// Drive window boundaries explicitly to measure backpressure independently of
// the wall-clock aggregation ticker. Every window uses the pipeline delivery path.
func BenchmarkProfilingMultiWindow(b *testing.B) {
	level := log.GetLevel()
	log.SetLevel("error")
	b.Cleanup(func() { log.SetLevel(level.String()) })
	for _, delay := range []time.Duration{0, time.Millisecond} {
		b.Run(fmt.Sprintf("windows=8/locks=1024/delay=%s", delay), func(b *testing.B) {
			samples := make([]aggregation.Sample, 1024)
			for i := range samples {
				samples[i] = aggregation.Sample{
					UserFrames: benchmarkStackFrames("lock", 32),
					Labels:     []types.ProfileLabel{types.ProfileLockAddressLabel(uint64(i))},
					Value:      1,
				}
			}

			destination := filepath.Join(b.TempDir(), "profile.out")
			var delivery time.Duration
			b.ReportAllocs()
			for b.Loop() {
				f, err := factory.NewFormatter(output.Config{
					Format:      output.FormatCollapsed,
					Destination: destination,
				})
				if err != nil {
					b.Fatal(err)
				}

				p := NewPipeline(&PipelineConfig{
					AggregationConfig: AggregationConfig{ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds"},
					Formatter: &benchmarkDelayedFormatter{
						Formatter: f,
						delay:     delay,
					},
				})
				for range 8 {
					p.windowStart = time.Now()
					for i := range samples {
						if err := p.aggregator.Add(&samples[i]); err != nil {
							b.Fatal(err)
						}
					}

					start := time.Now()
					if err := p.snapshot(b.Context(), start); err != nil {
						b.Fatal(err)
					}

					delivery += time.Since(start)
				}

				if err := f.Complete(b.Context()); err != nil {
					b.Fatal(err)
				}

				if err := f.Close(); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(delivery.Nanoseconds())/float64(b.N*8), "snapshot-ns/window")
		})
	}
}

type benchmarkDelayedFormatter struct {
	output.Formatter
	delay time.Duration
}

func (f *benchmarkDelayedFormatter) Add(ctx context.Context, w *types.AggregationWindow) error {
	time.Sleep(f.delay)
	return f.Formatter.Add(ctx, w)
}

// Isolate retained input from aggregation and output. Every batch owns its text;
// reusing a template would hide the memory cost of increasing queue capacity.
func BenchmarkPipelineQueuedBurst(b *testing.B) {
	for _, capacity := range []int{1, pipelineQueueCapacity} {
		b.Run(fmt.Sprintf("capacity=%d/text=1MiB", capacity), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				// No worker runs: measure a full queue at a stalled consumption boundary.
				p := &Pipeline{
					queue: make(chan queuedBatch, capacity),
				}
				for range capacity {
					batch := &SampleBatch{Samples: make([]aggregation.Sample, MaxBatchSamples)}
					for i := range batch.Samples {
						batch.Samples[i] = aggregation.Sample{
							UserFrames: []types.ProfileFrame{{Name: strings.Repeat("x", MaxBatchTextBytes/MaxBatchSamples)}},
							Value:      1,
						}
					}

					if err := p.Enqueue(b.Context(), batch); err != nil {
						b.Fatal(err)
					}
				}

				if err := p.Enqueue(b.Context(), testBatch(1)); !errors.Is(err, ErrQueueFull) {
					b.Fatalf("expected full queue error, got %v", err)
				}

				runtime.KeepAlive(p)
			}

			b.ReportMetric(float64(capacity*MaxBatchTextBytes), "queued-text-B/op")
		})
	}
}

func aggregationPipeline() *Pipeline {
	return NewPipeline(&PipelineConfig{AggregationConfig: AggregationConfig{
		ContainerID: "original-container",
		ProfileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
	}})
}

func benchmarkStackFrames(prefix string, depth int) []types.ProfileFrame {
	result := make([]types.ProfileFrame, depth)
	for i := range result {
		result[i].Name = prefix + "-frame-" + strconv.Itoa(i)
	}

	return result
}
