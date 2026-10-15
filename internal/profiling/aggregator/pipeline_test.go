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

package aggregator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiling/formatter/factory"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/profiling"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	"github.com/stretchr/testify/mock"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestNewPipelineConfig(t *testing.T) {
	for _, mode := range []AggregationMode{AggregationModeStreaming, AggregationModeOneShot} {
		cfg := PipelineConfig{AggregationMode: mode, TracerID: "task-123", Aggregator: NewMockAggregator(t)}
		p := NewPipeline(cfg)
		if p.aggrInterval != defaultAggregationInterval {
			t.Fatalf("interval = %s", p.aggrInterval)
		}
		if cfg.AggregationInterval != 0 {
			t.Fatal("constructor mutated config")
		}
		if cap(p.queue) != pipelineQueueCapacity {
			t.Fatalf("queue capacity = %d", cap(p.queue))
		}
		if p.mode != mode || p.tracerID != cfg.TracerID {
			t.Fatal("constructor did not preserve config")
		}
	}
	if p := NewPipeline(PipelineConfig{}); p.tracerID == "" {
		t.Fatal("missing generated tracer ID")
	}
}

func TestPipelineStart_IsIdempotent(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Snapshot", mock.Anything).Return(nil, nil).Once()

	p := NewPipeline(PipelineConfig{Aggregator: aggr, Formatter: testFormatter(t, output.FormatCollapsed, t.TempDir())})

	p.Start(t.Context())
	p.Start(t.Context())
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineStart_AfterStop(t *testing.T) {
	aggr := NewMockAggregator(t)

	p := NewPipeline(PipelineConfig{Aggregator: aggr, Formatter: testFormatter(t, output.FormatCollapsed, t.TempDir())})

	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	p.Start(t.Context())
	if err := p.Enqueue(t.Context(), "ignored"); err == nil {
		t.Fatal("accepted a record after Stop")
	}

	aggr.AssertNotCalled(t, "Aggregate")
}

func TestPipelineStop_DrainsAcceptedRecords(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Aggregate", "first").Once()
	aggr.On("Aggregate", "second").Once()
	aggr.On("Snapshot", mock.Anything).Return(nil, nil).Once()

	p := NewPipeline(PipelineConfig{Aggregator: aggr, Formatter: testFormatter(t, output.FormatCollapsed, t.TempDir())})
	p.Start(t.Context())
	if err := p.Enqueue(t.Context(), "first"); err != nil {
		t.Fatal(err)
	}
	if err := p.Enqueue(t.Context(), "second"); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineEnqueueFullQueue(t *testing.T) {
	for _, tc := range []struct {
		name               string
		mode               AggregationMode
		wantDroppedSamples int64
		wantErr            error
	}{
		{name: "streaming counts overflow", wantDroppedSamples: 1},
		{name: "one shot waits for cancellation", mode: AggregationModeOneShot, wantErr: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			aggr := NewMockAggregator(t)
			aggr.On("Aggregate", "first").Run(func(mock.Arguments) {
				close(entered)
				<-release
			}).Once()
			aggr.On("Aggregate", "queued").Once()
			p := NewPipeline(PipelineConfig{AggregationMode: tc.mode, Aggregator: aggr, Formatter: testFormatter(t, output.FormatCollapsed, t.TempDir())})
			p.queue = make(chan any, 1)
			p.Start(t.Context())
			t.Cleanup(func() { close(release); _ = p.Abort() })
			if err := p.Enqueue(t.Context(), "first"); err != nil {
				t.Fatal(err)
			}
			<-entered
			if err := p.Enqueue(t.Context(), "queued"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()
			if err := p.Enqueue(ctx, "rejected"); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Enqueue() error = %v, want %v", err, tc.wantErr)
			}
			if got := p.queueDroppedSamples.Load(); got != tc.wantDroppedSamples {
				t.Fatalf("queue dropped samples = %d, want %d", got, tc.wantDroppedSamples)
			}
		})
	}
}

func TestPipelineEnqueueRequiresRunningAndActiveContext(t *testing.T) {
	for _, mode := range []AggregationMode{AggregationModeStreaming, AggregationModeOneShot} {
		p := NewPipeline(PipelineConfig{AggregationMode: mode, Aggregator: NewMockAggregator(t), Formatter: testFormatter(t, output.FormatCollapsed, t.TempDir())})
		if err := p.Enqueue(t.Context(), "before start"); err == nil {
			t.Fatal("accepted a record before Start")
		}
		p.Start(t.Context())
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := p.Enqueue(ctx, "canceled"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Enqueue() error = %v, want cancellation", err)
		}
		if err := p.Abort(); err != nil {
			t.Fatal(err)
		}
		if err := p.Enqueue(t.Context(), "after stop"); err == nil {
			t.Fatal("accepted a record after Stop")
		}
	}
}

func TestPipelineReturnsExportFailure(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Snapshot", mock.Anything).Return(testWindow(3), nil).Once()
	wantErr := errors.New("store unavailable")
	notifications := 0
	formatter := &failingFormatter{err: wantErr}
	p := NewPipeline(PipelineConfig{
		AggregationMode: AggregationModeOneShot, Aggregator: aggr,
		Formatter: formatter, OnFailure: func() { notifications++ },
	})
	p.Start(t.Context())
	start := p.aggregationStartTime
	if err := p.Stop(); !errors.Is(err, wantErr) {
		t.Fatalf("Stop() error = %v, want %v", err, wantErr)
	}
	if !p.aggregationStartTime.Equal(start) {
		t.Fatal("failed export advanced aggregation time")
	}
	if notifications != 1 {
		t.Fatalf("failure notifications = %d, want 1", notifications)
	}
	if err := p.Stop(); !errors.Is(err, wantErr) {
		t.Fatalf("repeated Stop error = %v", err)
	}
	if notifications != 1 || formatter.completions != 0 {
		t.Fatalf("notifications = %d, completions = %d; want 1 and 0", notifications, formatter.completions)
	}
	aggr.AssertNotCalled(t, "Reset")
}

func TestPipelineAbortDoesNotExport(t *testing.T) {
	aggr := NewMockAggregator(t)
	p := NewPipeline(PipelineConfig{AggregationMode: AggregationModeOneShot, Aggregator: aggr, Formatter: testFormatter(t, output.FormatCollapsed, t.TempDir())})
	p.Start(t.Context())
	if err := p.Abort(); err != nil {
		t.Fatal(err)
	}
	aggr.AssertNotCalled(t, "Snapshot", mock.Anything)
}

func TestPipelineOneShotDrainsBatchAndWritesOneWindow(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Aggregate", "batch").Once()
	aggr.On("Snapshot", mock.Anything).Return(testWindow(9), nil).Once()
	aggr.On("Reset").Once()
	destination := filepath.Join(t.TempDir(), "profile.json")
	p := NewPipeline(PipelineConfig{AggregationMode: AggregationModeOneShot, Aggregator: aggr, Formatter: testFormatter(t, output.FormatJSON, destination)})
	p.Start(t.Context())
	if err := p.Enqueue(t.Context(), "batch"); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	var got types.ProfilingWindow
	if err := json.Unmarshal(readOutput(t, destination), &got); err != nil {
		t.Fatal(err)
	}
	if got.Profile.Sample[0].Value[0] != 9 {
		t.Fatalf("profile = %+v", got.Profile)
	}
	if err := p.Enqueue(t.Context(), "late"); err == nil {
		t.Fatal("accepted a batch after Stop")
	}
}

func TestPipelinePeriodicWindowKeepsRecordsArrivingDuringExport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipeline.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan *types.ProfilingWindow, 16)
	toolstream.Register(server, profiling.StoreServiceName, func(_ context.Context, request *toolstream.Request[types.ProfilingWindow]) error {
		if request.Data != nil {
			received <- request.Data
		}
		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	aggr := &windowCounter{entered: entered, release: release}
	p := NewPipeline(PipelineConfig{Aggregator: aggr, Formatter: testFormatter(t, output.FormatRemoteStore, path)})
	p.aggrInterval = time.Millisecond
	p.Start(t.Context())
	if err := p.Enqueue(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := p.Enqueue(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	var values []int64
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for len(values) < 2 {
		select {
		case window := <-received:
			values = append(values, window.Profile.Sample[0].Value[0])
		case <-timeout.C:
			t.Fatalf("received values=%v, want [1 2]", values)
		}
	}
	if values[0] != 1 || values[1] != 2 {
		t.Fatalf("values=%v, want [1 2]", values)
	}
}

type windowCounter struct {
	count            int64
	entered, release chan struct{}
	first            bool
}

func (a *windowCounter) Aggregate(record any) { a.count += int64(record.(int)) }
func (a *windowCounter) Snapshot(context.Context) (*types.ProfilingWindow, error) {
	if a.count == 0 {
		return nil, nil
	}
	if !a.first {
		a.first = true
		close(a.entered)
		<-a.release
	}
	return testWindow(a.count), nil
}
func (a *windowCounter) Reset() { a.count = 0 }

func testWindow(value int64) *types.ProfilingWindow {
	return &types.ProfilingWindow{ProfileType: "irqtracing:irq:count:irq:count", Profile: &profilev1.Profile{
		TimeNanos: 123, DurationNanos: int64(time.Second), StringTable: []string{""},
		Sample: []*profilev1.Sample{{Value: []int64{value}}},
	}}
}

func TestExportAggregationWindowReportsDroppedSamples(t *testing.T) {
	for _, tc := range []struct {
		name       string
		collection uint64
		queue      int64
		failLog    bool
	}{
		{name: "no loss"},
		{name: "collection loss", collection: 3},
		{name: "queue loss", queue: 5},
		{name: "both reasons", collection: 3, queue: 5},
		{name: "diagnostic write failure", collection: 3, failLog: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			destination := filepath.Join(t.TempDir(), "profile.json")
			var diagnosticWriter io.Writer = &diagnostics
			if tc.failLog {
				diagnosticWriter = failedDiagnosticWriter{}
			}
			level := log.GetLevel()
			log.SetLevel("warn")
			log.SetOutput(diagnosticWriter)
			t.Cleanup(func() { log.SetOutput(os.Stdout); log.SetLevel(level.String()) })
			window := &types.ProfilingWindow{
				Profile:        &profilev1.Profile{},
				DroppedSamples: map[string]uint64{types.DropReasonCollection: tc.collection},
			}
			aggr := new(MockAggregator)
			aggr.On("Snapshot", mock.Anything).Return(window, nil).Once()
			aggr.On("Reset").Once()
			pipeline := NewPipeline(PipelineConfig{TracerID: "loss-task", Aggregator: aggr, Formatter: testFormatter(t, output.FormatJSON, destination)})
			pipeline.aggregationStartTime = time.Now()
			pipeline.queueDroppedSamples.Store(tc.queue)
			if err := pipeline.exportAggregationWindow(t.Context()); err != nil {
				t.Fatal(err)
			}
			aggr.AssertExpectations(t)
			var result types.ProfilingWindow
			if err := json.Unmarshal(readOutput(t, destination), &result); err != nil {
				t.Fatal(err)
			}
			if result.DroppedSamples[types.DropReasonCollection] != tc.collection ||
				result.DroppedSamples[types.DropReasonAggregationQueue] != uint64(tc.queue) {
				t.Fatalf("exported losses = %v", result.DroppedSamples)
			}
			if tc.failLog {
				return
			}
			want := 0
			if tc.collection != 0 || tc.queue != 0 {
				want = 1
			}
			if got := strings.Count(diagnostics.String(), "profiling samples dropped; profile is incomplete"); got != want {
				t.Fatalf("warning count = %d, want %d: %s", got, want, diagnostics.String())
			}
			if want != 0 && (!strings.Contains(diagnostics.String(), "loss-task") || !strings.Contains(diagnostics.String(), "dropped_samples")) {
				t.Fatalf("warning lacks task or loss metadata: %s", diagnostics.String())
			}
		})
	}
}

type failedDiagnosticWriter struct{}

func (failedDiagnosticWriter) Write([]byte) (int, error) {
	return 0, errors.New("diagnostic output unavailable")
}

func testFormatter(t *testing.T, format output.OutputFormat, destination string) output.Formatter {
	t.Helper()
	formatter, err := factory.NewFormatter(output.FormatterConfig{Format: format, Destination: destination, TaskID: "task", ToolName: "test"})
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

type failingFormatter struct {
	err         error
	completions int
}

func (f *failingFormatter) Add(context.Context, *types.ProfilingWindow) error { return f.err }
func (f *failingFormatter) Complete(context.Context) error                    { f.completions++; return f.err }

func TestPipelineFinalSnapshotUsesRuntimeContext(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "task"))
	defer cancel()
	aggr := NewMockAggregator(t)
	aggr.On("Snapshot", mock.MatchedBy(func(ctx context.Context) bool {
		return ctx.Err() == nil && ctx.Value(contextKey{}) == "task"
	})).Return(nil, nil).Once()
	p := NewPipeline(PipelineConfig{
		AggregationMode: AggregationModeOneShot,
		Aggregator:      aggr,
		Formatter:       testFormatter(t, output.FormatCollapsed, t.TempDir()),
	})
	p.Start(ctx)
	cancel()
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineOwnsAggregationTime(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode AggregationMode
	}{
		{name: "streaming", mode: AggregationModeStreaming},
		{name: "one shot", mode: AggregationModeOneShot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := testWindow(1), testWindow(2)
			aggr := NewMockAggregator(t)
			aggr.On("Snapshot", mock.Anything).Return(first, nil).Once()
			aggr.On("Snapshot", mock.Anything).Return(second, nil).Once()
			aggr.On("Snapshot", mock.Anything).Return(nil, nil).Once()
			aggr.On("Reset").Twice()
			p := NewPipeline(PipelineConfig{
				AggregationMode: tc.mode, AggregationInterval: time.Hour, Aggregator: aggr,
				Formatter: testFormatter(t, output.FormatJSON, t.TempDir()),
			})
			if !p.aggregationStartTime.IsZero() {
				t.Fatal("constructor initialized aggregation time before Start")
			}
			beforeStart := time.Now()
			p.Start(t.Context())
			t.Cleanup(func() { _ = p.Stop() })
			start := p.aggregationStartTime
			if start.Before(beforeStart) || start.After(time.Now()) {
				t.Fatal("Start did not initialize aggregation time")
			}
			for _, window := range []*types.ProfilingWindow{first, second} {
				if err := p.exportAggregationWindow(t.Context()); err != nil {
					t.Fatal(err)
				}
				end := p.aggregationStartTime
				if window.Profile.TimeNanos != start.UnixNano() {
					t.Fatalf("start = %d, want %d", window.Profile.TimeNanos, start.UnixNano())
				}
				if window.Profile.DurationNanos != end.Sub(start).Nanoseconds() || window.Profile.DurationNanos <= 0 {
					t.Fatalf("duration = %d, want %d", window.Profile.DurationNanos, end.Sub(start).Nanoseconds())
				}
				start = end
			}
			if err := p.Stop(); err != nil {
				t.Fatal(err)
			}
			if !p.aggregationStartTime.Equal(start) {
				t.Fatal("empty snapshot advanced aggregation time")
			}
		})
	}
}

func (f *failingFormatter) Close() error { return nil }

func TestPipelineCompletesFormatterOnce(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Snapshot", mock.Anything).Return(nil, nil).Once()
	formatter := &failingFormatter{}
	p := NewPipeline(PipelineConfig{AggregationMode: AggregationModeOneShot, Aggregator: aggr, Formatter: formatter})
	p.Start(t.Context())
	for range 2 {
		if err := p.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	if formatter.completions != 1 {
		t.Fatalf("Complete calls = %d, want 1", formatter.completions)
	}
}
