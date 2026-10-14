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
	"github.com/ccfos/huatuo/internal/toolstream"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	"github.com/stretchr/testify/mock"

	profctx "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestNewPipeline_DoesNotMutateContext(t *testing.T) {
	pctx := &profctx.ProfilerContext{}

	p := NewPipeline(pctx, NewMockAggregator(t))

	if pctx.AggrInterval != 0 {
		t.Fatalf("NewPipeline mutated AggrInterval to %d", pctx.AggrInterval)
	}

	if p.aggrInterval != 10*time.Second {
		t.Fatalf("NewPipeline aggrInterval = %s, want 10s", p.aggrInterval)
	}
}

func TestResolveTracerID(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		allocate   func() (string, error)
		want       string
		wantAlloc  bool
	}{
		{
			name:       "configured ID",
			configured: "trace-123",
			allocate: func() (string, error) {
				return "unexpected", nil
			},
			want: "trace-123",
		},
		{
			name: "allocated ID",
			allocate: func() (string, error) {
				return "generated-123", nil
			},
			want:      "generated-123",
			wantAlloc: true,
		},
		{
			name: "allocation error",
			allocate: func() (string, error) {
				return "", errors.New("random source unavailable")
			},
			wantAlloc: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocated := false
			allocate := func() (string, error) {
				allocated = true
				return tt.allocate()
			}
			if got := resolveTracerID(tt.configured, allocate); got != tt.want {
				t.Fatalf("resolveTracerID() = %q, want %q", got, tt.want)
			}
			if allocated != tt.wantAlloc {
				t.Fatalf("allocator called = %t, want %t", allocated, tt.wantAlloc)
			}
		})
	}
}

func TestPipelineStart_IsIdempotent(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Snapshot", mock.Anything).Return(nil, nil).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	p.Start()
	p.Start()
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineStart_AfterStop(t *testing.T) {
	aggr := NewMockAggregator(t)

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	p.Start()
	p.Enqueue("ignored")

	aggr.AssertNotCalled(t, "Aggregate")
}

func TestPipelineEnqueue_AfterStop(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Snapshot", mock.Anything).Return(nil, nil).Maybe()

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	p.Enqueue("ignored")

	aggr.AssertNotCalled(t, "Aggregate")
}

func TestPipelineStop_DrainsAcceptedRecords(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Aggregate", "first").Once()
	aggr.On("Aggregate", "second").Once()
	aggr.On("Snapshot", mock.Anything).Return(nil, nil).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          t.Context(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)
	p.Enqueue("first")
	p.Enqueue("second")

	p.Start()
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineEnqueue_CountsOverflow(t *testing.T) {
	p := NewPipeline(&profctx.ProfilerContext{}, NewMockAggregator(t))

	for i := 0; i < pipelineQueueCapacity+1; i++ {
		p.Enqueue(i)
	}

	if got := len(p.queue); got != pipelineQueueCapacity {
		t.Fatalf("len(p.queue) = %d, want %d", got, pipelineQueueCapacity)
	}
	if got := p.overflowCount.Load(); got != 1 {
		t.Fatalf("p.overflowCount.Load() = %d, want 1", got)
	}
}

func TestPipelineReturnsExportFailure(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Snapshot", mock.Anything).Return(testWindow(3), nil).Once()
	p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context(), IsOneShotAgg: true, OutputFormat: output.FormatRemoteStore}, aggr)
	p.Start()
	if err := p.Stop(); err == nil {
		t.Fatal("missing toolstream client reported success")
	}
	if err := p.Stop(); err == nil {
		t.Fatal("repeated Stop lost the failure")
	}
	aggr.AssertNotCalled(t, "Reset")
}

func TestPipelineAbortDoesNotExport(t *testing.T) {
	aggr := NewMockAggregator(t)
	p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context(), IsOneShotAgg: true}, aggr)
	p.Start()
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
	var destination bytes.Buffer
	p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context(), IsOneShotAgg: true, OutputFormat: output.FormatJSON, OutputWriter: &destination}, aggr)
	p.Start()
	if err := p.EnqueueBatch(t.Context(), "batch"); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	var got types.ProfilingWindow
	if err := json.Unmarshal(destination.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Profile.Sample[0].Value[0] != 9 {
		t.Fatalf("profile = %+v", got.Profile)
	}
	if err := p.EnqueueBatch(t.Context(), "late"); err == nil {
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
	toolstream.Register(server, "test", func(_ context.Context, request *toolstream.Request[types.ProfilingWindow]) error {
		if request.Data != nil {
			received <- request.Data
		}
		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	}()
	client, err := toolstream.NewClient(toolstream.ClientOptions{SockPath: path, ToolName: "test", TaskID: "task", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	}()
	entered := make(chan struct{})
	release := make(chan struct{})
	aggr := &windowCounter{entered: entered, release: release}
	p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context(), OutputFormat: output.FormatRemoteStore, ToolstreamClient: client}, aggr)
	p.aggrInterval = time.Millisecond
	p.Enqueue(1)
	p.Start()
	<-entered
	p.Enqueue(2)
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
func (a *windowCounter) Snapshot(*profctx.ProfilerContext) (*types.ProfilingWindow, error) {
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

func TestExportSnapshotReportsDroppedSamples(t *testing.T) {
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
			var diagnostics, destination bytes.Buffer
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
			pipeline := NewPipeline(&profctx.ProfilerContext{
				Ctx: t.Context(), TracerID: "loss-task", OutputFormat: output.FormatJSON, OutputWriter: &destination,
			}, aggr)
			pipeline.overflowCount.Store(tc.queue)
			if err := pipeline.exportSnapshot(t.Context()); err != nil {
				t.Fatal(err)
			}
			aggr.AssertExpectations(t)
			var result types.ProfilingWindow
			if err := json.Unmarshal(destination.Bytes(), &result); err != nil {
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
