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
	"maps"
	"slices"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/toolstream"
)

func TestProfileCollectionLabels(t *testing.T) {
	tests := []struct {
		name string
		pctx *profctx.ProfilerContext
		want map[string]string
	}{
		{
			name: "host",
			pctx: &profctx.ProfilerContext{},
			want: map[string]string{
				profiler.LabelProfilingScope: "host",
			},
		},
		{
			name: "CPU",
			pctx: &profctx.ProfilerContext{CPUIDs: []int{6, 2, 6}},
			want: map[string]string{
				profiler.LabelProfilingScope: "cpu",
				profiler.LabelCPU:            "2,6",
			},
		},
		{
			name: "PID with CPU restriction",
			pctx: &profctx.ProfilerContext{
				PIDs:   []int{42, 7, 42},
				CPUIDs: []int{5, 3},
			},
			want: map[string]string{
				profiler.LabelProfilingScope: "pid",
				profiler.LabelCPU:            "3,5",
				profiler.LabelPID:            "7,42",
			},
		},
		{
			name: "thread group uses TGID",
			pctx: &profctx.ProfilerContext{
				PIDs:        []int{42, 7},
				ThreadGroup: true,
			},
			want: map[string]string{
				profiler.LabelProfilingScope: "thread_group",
				profiler.LabelTGID:           "7,42",
			},
		},
		{
			name: "container takes scope precedence",
			pctx: &profctx.ProfilerContext{
				ContainerID: "container-a",
				PIDs:        []int{42, 7},
				CPUIDs:      []int{5, 3},
				ThreadGroup: true,
			},
			want: map[string]string{
				profiler.LabelProfilingScope: "container",
				profiler.LabelContainerID:    "container-a",
				profiler.LabelCPU:            "3,5",
				profiler.LabelTGID:           "7,42",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := profileCollectionLabels(test.pctx)
			if !maps.Equal(got, test.want) {
				t.Fatalf("profileCollectionLabels() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSetProfileCollectionLabelsRebuildsManagedDimensions(t *testing.T) {
	data := &profiler.ProfileData{Labels: map[string]string{
		"custom":                     "retained",
		profiler.LabelProfilingScope: "stale",
		profiler.LabelCPU:            "stale",
		profiler.LabelPID:            "stale",
		profiler.LabelTGID:           "stale",
		profiler.LabelContainerID:    "stale",
	}}
	pids := []int{42, 7, 42}
	cpus := []int{6, 2, 6}
	pctx := &profctx.ProfilerContext{
		PIDs:        pids,
		CPUIDs:      cpus,
		ThreadGroup: true,
	}

	setProfileCollectionLabels(data, pctx)

	want := map[string]string{
		"custom":                     "retained",
		profiler.LabelProfilingScope: "thread_group",
		profiler.LabelCPU:            "2,6",
		profiler.LabelTGID:           "7,42",
	}
	if !maps.Equal(data.Labels, want) {
		t.Fatalf("ProfileData.Labels = %v, want %v", data.Labels, want)
	}
	if !slices.Equal(pctx.PIDs, []int{42, 7, 42}) ||
		!slices.Equal(pctx.CPUIDs, []int{6, 2, 6}) {
		t.Fatalf("context ID order mutated: PIDs=%v CPUIDs=%v", pctx.PIDs, pctx.CPUIDs)
	}
}

type savedProfileEvent struct {
	TracerData struct {
		FlameData *profiler.ProfileData `json:"flamedata"`
	} `json:"tracer_data"`
}

func TestSaveProfilingDocumentInjectsLabelsAtUploadBoundary(t *testing.T) {
	sockPath := t.TempDir() + "/toolstream.sock"
	server, err := toolstream.NewServer(sockPath)
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}
	received := make(chan *savedProfileEvent, 1)
	toolstream.Register(
		server,
		profilerTracerName,
		func(_ *toolstream.Session, event *savedProfileEvent) error {
			received <- event
			return nil
		},
	)
	if err := server.Start(); err != nil {
		t.Fatalf("toolstream server start: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })

	client, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: sockPath,
		ToolName: profilerTracerName,
		Version:  "1",
		TaskID:   "trace-a",
	})
	if err != nil {
		t.Fatalf("toolstream.NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	pctx := &profctx.ProfilerContext{
		PIDs:             []int{42, 7},
		CPUIDs:           []int{6, 2},
		ThreadGroup:      true,
		ToolstreamClient: client,
	}
	pipeline := &Pipeline{pctx: pctx, tracerID: "trace-a"}
	data := &profiler.ProfileData{Labels: map[string]string{"custom": "retained"}}
	if err := pipeline.saveProfilingDocument(context.Background(), data); err != nil {
		t.Fatalf("saveProfilingDocument() error = %v", err)
	}

	select {
	case event := <-received:
		want := map[string]string{
			"custom":                     "retained",
			profiler.LabelProfilingScope: "thread_group",
			profiler.LabelCPU:            "2,6",
			profiler.LabelTGID:           "7,42",
		}
		if event == nil || event.TracerData.FlameData == nil {
			t.Fatal("received profile event without flame data")
		}
		if got := event.TracerData.FlameData.Labels; !maps.Equal(got, want) {
			t.Fatalf("uploaded labels = %v, want %v", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for uploaded profile event")
	}
}
