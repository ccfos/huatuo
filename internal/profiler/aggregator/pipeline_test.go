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
	"context"
	"errors"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
	"huatuo-bamai/internal/toolstream"
)

type pipelineProfileEvent struct {
	TracerData *profctx.TracerData `json:"tracer_data"`
}

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
	aggr.On("OutputFormatter").Return(nil).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	p.Start()
	p.Start()
	p.Stop()
}

func TestPipelineStart_AfterStop(t *testing.T) {
	aggr := NewMockAggregator(t)

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	p.Stop()
	p.Start()
	p.Enqueue("ignored")

	aggr.AssertNotCalled(t, "Aggregate")
}

func TestPipelineEnqueue_AfterStop(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("OutputFormatter").Return(nil).Maybe()

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          context.Background(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	p.Stop()
	p.Enqueue("ignored")

	aggr.AssertNotCalled(t, "Aggregate")
}

func TestPipelineStop_DrainsAcceptedRecords(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("Aggregate", "first").Once()
	aggr.On("Aggregate", "second").Once()
	aggr.On("OutputFormatter").Return(nil).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          t.Context(),
		OutputFormat: output.FormatCollapsed,
	}, aggr)
	p.Enqueue("first")
	p.Enqueue("second")

	p.Start()
	p.Stop()
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

func TestPipelineAggregateAndExport_NilFormatter(t *testing.T) {
	aggr := NewMockAggregator(t)
	aggr.On("OutputFormatter").Return(nil).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		OutputFormat: output.FormatCollapsed,
	}, aggr)

	err := p.aggregateAndSnapshot(context.Background(), true)
	if err == nil {
		t.Fatal("aggregateAndSnapshot returned nil error")
	}

	const want = `output formatter is nil for non-upload format "collapsed"`
	if got := err.Error(); got != want {
		t.Fatalf("aggregateAndSnapshot error = %q, want %q", got, want)
	}
}

func TestPipelineAggregateAndExport_EmptyFormatter(t *testing.T) {
	formatter := NewFormatter(t)
	formatter.On("IsEmpty").Return(true).Once()

	aggr := NewMockAggregator(t)
	aggr.On("OutputFormatter").Return(formatter).Once()

	p := NewPipeline(&profctx.ProfilerContext{
		OutputFormat: output.FormatCollapsed,
		OutputPath:   t.TempDir(),
	}, aggr)

	if err := p.aggregateAndSnapshot(context.Background(), true); err != nil {
		t.Fatalf("aggregateAndSnapshot returned error: %v", err)
	}

	aggr.AssertNotCalled(t, "Reset")
}

func TestPipelineAggregateAndSnapshotInjectsCollectionDimensionLabels(t *testing.T) {
	sockPath := t.TempDir() + "/toolstream.sock"
	server, err := toolstream.NewServer(sockPath)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	events := make(chan pipelineProfileEvent, 1)
	toolstream.Register(
		server,
		"profiler",
		func(_ *toolstream.Session, event pipelineProfileEvent) error {
			events <- event
			return nil
		},
	)
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start() error = %v", err)
	}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("server.Close() error = %v", err)
		}
	})

	client, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: sockPath,
		ToolName: "profiler",
		Version:  "1",
		TaskID:   "profile-label-test",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.End)

	data, err := profiler.ParseTree(
		time.Unix(1, 0),
		profiler.ProfileTypeCpuSample,
		[]*profiler.TreeItem{{Stack: [][]byte{[]byte("work")}, Value: 1}},
		&profiler.ParseOption{SampleRate: 99},
	)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}

	pctx := &profctx.ProfilerContext{
		Ctx:              t.Context(),
		PIDs:             []int{42},
		ThreadGroup:      true,
		OutputFormat:     output.FormatRemote,
		TracerID:         "profile-label-test",
		ToolstreamClient: client,
	}
	aggr := NewMockAggregator(t)
	aggr.On("Snapshot", pctx).Return(data, nil).Once()
	aggr.On("Reset").Once()
	pipeline := NewPipeline(pctx, aggr)

	if err := pipeline.aggregateAndSnapshot(t.Context(), false); err != nil {
		t.Fatalf("aggregateAndSnapshot() error = %v", err)
	}

	select {
	case event := <-events:
		flameData := event.TracerData.FlameData
		if got := flameData.Labels[profiler.LabelProfilingScope]; got != "thread_group" {
			t.Fatalf("profiling_scope = %q, want thread_group", got)
		}
		if got := flameData.Labels[profiler.LabelTGID]; got != "42" {
			t.Fatalf("tgid = %q, want 42", got)
		}
		if len(flameData.Profile.Sample) != 1 {
			t.Fatalf("samples = %d, want 1", len(flameData.Profile.Sample))
		}
		got := make(map[string]string)
		for _, label := range flameData.Profile.Sample[0].Label {
			got[flameData.Profile.StringTable[label.Key]] = flameData.Profile.StringTable[label.Str]
		}
		if got[profiler.LabelProfilingScope] != "thread_group" ||
			got[profiler.LabelTGID] != "42" {
			t.Fatalf("sample labels = %#v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for profiling event")
	}
}
