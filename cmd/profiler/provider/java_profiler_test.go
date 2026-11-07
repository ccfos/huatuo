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

package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	javaruntime "github.com/ccfos/huatuo/cmd/profiler/runtime/java"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	"github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestNativeProfilersRejectMultiplePIDs(t *testing.T) {
	t.Parallel()

	cfg := &Config{PIDs: []int{123, 456}}
	tests := []struct {
		name      string
		start     func(context.Context) error
		wantError string
	}{
		{
			name:      "CPU",
			start:     (&cpuNativeProfiler{config: *cfg}).Start,
			wantError: "start native CPU profiler: multiple PIDs are not supported",
		},
		{
			name:      "memory",
			start:     (&memNativeProfiler{config: *cfg}).Start,
			wantError: "start native memory profiler: multiple PIDs are not supported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.start(t.Context())
			if err == nil || err.Error() != tt.wantError {
				t.Fatalf("Start() error=%v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestJavaProfilerCapturesConfiguration(t *testing.T) {
	cfg := &Config{
		Type:        profiling.TypeCPU,
		Freq:        100,
		ContainerID: "original-container",
	}
	task, err := New(profiling.ImplementationJava, cfg)
	if err != nil {
		t.Fatal(err)
	}

	cfg.Freq = 1
	cfg.ContainerID = "changed-container"
	if task.AggregationConfig.ContainerID != "original-container" || task.Impl.(*javaProfiler).config.Freq != 100 {
		t.Fatal("task aliases configuration")
	}
}

func TestPrepareJavaSamplingRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	toolDir := t.TempDir()
	for _, name := range []string{"bin/asprof", "lib/libasyncProfiler.so"} {
		path := filepath.Join(toolDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		if name == "bin/asprof" {
			if err := os.Chmod(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name      string
		toolDir   string
		wantError string
	}{
		{
			name:      "missing tools",
			wantError: "tool directory is not configured",
		},
		{
			name:      "non-java process",
			toolDir:   toolDir,
			wantError: `want prefix "java"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pids, labels, err := prepareJavaSampling(&Config{
				ToolDir: tc.toolDir,
				PIDs:    []int{os.Getpid()},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("prepareJavaSampling() error=%v, want %q", err, tc.wantError)
			}
			if pids != nil || labels != nil {
				t.Fatalf("failed preparation returned pids=%v labels=%v", pids, labels)
			}
		})
	}
}

func TestJavaProfilerSampleWeights(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		config Config
		want   int64
	}{
		{
			name: "cpu nanoseconds",
			config: Config{
				Type: profiling.TypeCPU,
				Freq: 99,
			},
			want: 70000000,
		},
		{
			name: "allocated bytes",
			config: Config{
				Type: profiling.TypeMemory,
				Mode: profiling.ModeObjectAlloc,
			},
			want: 7,
		},
		{
			name: "live bytes",
			config: Config{
				Type: profiling.TypeMemory,
				Mode: profiling.ModeObjectUsage,
			},
			want: 7,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			task, err := New(profiling.ImplementationJava, &tc.config)
			if err != nil {
				t.Fatal(err)
			}
			p, ok := task.Impl.(*javaProfiler)
			if !ok {
				t.Fatalf("Java implementation = %T, want *javaProfiler", task.Impl)
			}
			path := filepath.Join(t.TempDir(), "profile-0.collapsed")
			if err := os.WriteFile(path, []byte("root;leaf 7\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			// No external sampler is active; normal stopping drains this final file.
			p.samplingOpt = &javaruntime.AsprofSamplingOption{Pids: []int{123}}
			p.profileOutFile = map[int]string{123: path}
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(profile.ErrCollectionDurationElapsed)
			var count int
			enqueue := func(batch *pipeline.SampleBatch) error {
				for i := range batch.Samples {
					count++
					if got := batch.Samples[i].Value; got != tc.want {
						t.Errorf("sample weight = %d, want %d", got, tc.want)
					}
				}
				return nil
			}
			if err := p.ReadDataLoop(ctx, func(*pipeline.SampleBatch) error {
				t.Fatal("final sample delivered to read-loop sink")
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			if err := p.Finalize(t.Context(), enqueue); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("sample count = %d, want 1", count)
			}
		})
	}
}

// javaWindowOutput exposes completed windows without racing the pipeline worker.
type javaWindowOutput struct {
	windows chan *types.AggregationWindow
}

func (*javaWindowOutput) Name() output.OutputFormat { return output.FormatJSON }

func (f *javaWindowOutput) Add(ctx context.Context, w *types.AggregationWindow) error {
	select {
	case f.windows <- w:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*javaWindowOutput) Complete(context.Context) error { return nil }
func (*javaWindowOutput) Close() error                   { return nil }

func TestJavaProfilerStreamsAndDrainsFinalFile(t *testing.T) {
	for _, cfg := range []Config{
		{
			Type: profiling.TypeCPU,
			Freq: 100,
		},
		{
			Type: profiling.TypeMemory,
			Mode: profiling.ModeObjectAlloc,
		},
		{
			Type: profiling.TypeMemory,
			Mode: profiling.ModeObjectUsage,
		},
	} {
		t.Run(string(cfg.Type)+"/"+string(cfg.Mode), func(t *testing.T) {
			t.Parallel()
			task, err := New(profiling.ImplementationJava, &cfg)
			if err != nil {
				t.Fatal(err)
			}

			dir := t.TempDir()
			writeFile := func(sequence, value int) {
				t.Helper()
				path := filepath.Join(dir, fmt.Sprintf("profile-%d.collapsed", sequence))
				if err := os.WriteFile(path, fmt.Appendf(nil, "root;leaf %d\n", value), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			p := task.Impl.(*javaProfiler)
			p.samplingOpt = &javaruntime.AsprofSamplingOption{Pids: []int{123}}
			p.profileOutFile = map[int]string{123: filepath.Join(dir, "profile-*.collapsed")}
			p.labels = map[int][]types.ProfileLabel{123: {types.ProfileStringLabel("pid", "123")}}
			output := &javaWindowOutput{windows: make(chan *types.AggregationWindow)}
			pipe := pipeline.NewPipeline(&pipeline.PipelineConfig{
				AggregationConfig:   task.AggregationConfig,
				AggregationMode:     task.AggregationMode,
				AggregationInterval: 10 * time.Millisecond,
				Formatter:           output,
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			collectCtx, stop := context.WithCancelCause(ctx)
			defer stop(context.Canceled)
			pipelineDone := make(chan error, 1)
			readerDone := make(chan error, 1)
			go func() { pipelineDone <- pipe.Run(ctx) }()
			go func() {
				readerDone <- p.ReadDataLoop(collectCtx, func(batch *pipeline.SampleBatch) error {
					return pipe.Enqueue(ctx, batch)
				})
			}()
			defer func() {
				cancel()
				if readerDone != nil {
					<-readerDone
				}
				if pipelineDone != nil {
					<-pipelineDone
				}
			}()

			checkWindow := func(want int64) {
				t.Helper()
				select {
				case w := <-output.windows:
					if cfg.Type == profiling.TypeCPU {
						want *= int64(10 * time.Millisecond)
					}
					if len(w.Aggregated.Samples) != 1 || w.Aggregated.Samples[0].Value != want {
						t.Fatalf("samples = %+v, want one sample with weight %d", w.Aggregated.Samples, want)
					}
					if len(w.Aggregated.LabelSets) != 1 || len(w.Aggregated.LabelSets[0]) != 1 || w.Aggregated.LabelSets[0][0].StringValue != "123" {
						t.Fatalf("labels = %+v, want PID 123", w.Aggregated.LabelSets)
					}
				case <-ctx.Done():
					t.Fatal("timed out waiting for Java aggregation window")
				}
			}
			writeFile(0, 7)
			checkWindow(7)
			// A normal stop must consume a file that has not passed stability polling.
			writeFile(1, 3)
			stop(profile.ErrCollectionDurationElapsed)
			err = <-readerDone
			readerDone = nil
			if err != nil {
				t.Fatal(err)
			}

			if err := p.Finalize(ctx, func(batch *pipeline.SampleBatch) error { return pipe.Enqueue(ctx, batch) }); err != nil {
				t.Fatal(err)
			}

			pipe.Close()
			checkWindow(3)
			err = <-pipelineDone
			pipelineDone = nil
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
