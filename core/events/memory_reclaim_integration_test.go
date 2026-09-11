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

//go:build integration && linux

package events

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/document"
	_ "github.com/ccfos/huatuo/internal/storage/localfile"
	"github.com/ccfos/huatuo/internal/tracing"
	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
	"github.com/ccfos/huatuo/pkg/types"
)

// This is an attach smoke test, not a global-memory-pressure trigger test.
func TestReclaimLiveAttach(t *testing.T) {
	dir := os.Getenv("HUATUO_TRIGGER_BPF_DIR")
	if dir == "" {
		t.Skip("set HUATUO_TRIGGER_BPF_DIR on a test VM")
	}
	if err := bpf.Init(nil); err != nil {
		t.Fatal(err)
	}
	defer bpf.Shutdown()
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = dir
	defer func() { bpf.DefaultObjDir = previous }()
	obj, err := bpf.LoadBPF("memory_reclaim_events.o", map[string]any{
		"reclaim_duration_threshold_ns": uint64(900000000),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer obj.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, err := obj.AttachAndEventPipe(ctx, "reclaim_perf_events", 8192)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	t.Log("existing reclaim probes attached; no global memory pressure generated")
}

// A fixture proves host-stream serialization and persistence, not BPF delivery.
func TestReclaimHostPersistence(t *testing.T) {
	dir := t.TempDir()
	store, err := tracingstore.NewFromConfig(context.Background(), tracingstore.Config{
		LocalFile: &tracingstore.LocalFileConfig{Path: dir, RotationSizeMiB: 10, MaxRotatedFiles: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if err := tracing.EnableDocumentWriter(store, document.New("")); err != nil {
		t.Fatal(err)
	}
	defer tracing.DisableDocumentWriter()
	id, attribution := reclaimEventTarget(nil)
	if err := tracing.Save(&tracing.WriteRequest{
		TracerName: "memory_reclaim", ContainerID: id, ObservedTimestamp: time.Now().UTC(),
		TracerData: &MemoryReclaimTracingData{
			PID: 42, TID: 43, Comm: "fixture",
			ReclaimDurationNS: 900000001, ContainerAttribution: attribution,
		},
	}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "memory_reclaim"))
	if err != nil {
		t.Fatal(err)
	}
	var doc tracingstore.Document
	if err := json.Unmarshal(contents, &doc); err != nil {
		t.Fatal(err)
	}
	data := doc.TracerData.(map[string]any)
	if doc.TracerName != "memory_reclaim" || doc.ContainerID != "" ||
		doc.TracerRunType != types.TracerRunTypeEvent ||
		data["container_attribution"] != "unresolved" ||
		data["reclaim_duration_ns"] != float64(900000001) {
		t.Fatalf("unexpected persisted fixture: %+v", doc)
	}
}
