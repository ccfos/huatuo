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

package autotracing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"

	cgroupV2 "github.com/ccfos/huatuo/internal/cgroups/v2"

	_ "github.com/ccfos/huatuo/internal/storage/localfile"

	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
)

func initDloadLiveBPF(t *testing.T) {
	t.Helper()
	dir := os.Getenv("HUATUO_TRIGGER_BPF_DIR")
	if dir == "" {
		t.Skip("set HUATUO_TRIGGER_BPF_DIR on a test VM")
	}
	if err := bpf.Init(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bpf.Shutdown)
	previous := bpf.DefaultObjDir
	bpf.DefaultObjDir = dir
	t.Cleanup(func() { bpf.DefaultObjDir = previous })
	t.Cleanup(func() {
		if err := cgroupV2.CloseLoadStats(); err != nil {
			t.Error(err)
		}
	})
}

func dloadLiveStore(t *testing.T) func() ([]*tracingstore.Document, error) {
	t.Helper()
	dir := t.TempDir()
	store, err := tracingstore.NewFromConfig(context.Background(), tracingstore.Config{
		LocalFile: &tracingstore.LocalFileConfig{Path: dir, RotationSizeMiB: 10, MaxRotatedFiles: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tracing.EnableDocumentWriter(store, document.New("")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		tracing.DisableDocumentWriter()
		if err := store.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return func() ([]*tracingstore.Document, error) {
		file, err := os.Open(filepath.Join(dir, "dload"))
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		defer file.Close()
		var docs []*tracingstore.Document
		decoder := json.NewDecoder(file)
		for {
			var doc tracingstore.Document
			if err := decoder.Decode(&doc); err == io.EOF {
				return docs, nil
			} else if err != nil {
				return nil, err
			}
			docs = append(docs, &doc)
		}
	}
}

func TestDloadHostLiveCapture(t *testing.T) {
	initDloadLiveBPF(t)
	store := dloadLiveStore(t)
	cfg := &Config{}
	cfg.Dload.EnableDebug = true
	cfg.Dload.Interval = 10
	cfg.Dload.IntervalTracing = 30
	d, err := newDloadTracing(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.v2LoadStats(nil); err != nil {
		t.Fatal(err)
	}
	if d.hostStats == nil || d.hostStats.NrSleeping == 0 {
		t.Fatal("missing whole-host snapshot")
	}
	stack, err := dumpUninterruptibleTaskStack(taskScopeHost, "", true)
	if err != nil || stack == "" {
		t.Fatal("missing host debug stacks", err)
	}
	now := time.Now()
	d.traceHost(now, &dloadStackCapture{dump: dumpUninterruptibleTaskStack})
	if !d.host.lastTraceAt.Equal(now) {
		t.Fatal("host did not complete independent trace")
	}
	documents, err := store()
	if err != nil || len(documents) != 1 {
		t.Fatalf("persisted debug traces: %d, error: %v", len(documents), err)
	}
}

// The caller supplies a bounded D-state worker in a disposable VM. Debug mode
// must stay off: this exercises the normal sampling, threshold and save path.
func TestDloadHostLiveTrigger(t *testing.T) {
	pid, err := strconv.Atoi(os.Getenv("HUATUO_DLOAD_WORKER_PID"))
	if err != nil || pid <= 1 {
		t.Skip("set HUATUO_DLOAD_WORKER_PID to a bounded D-state test worker")
	}
	initDloadLiveBPF(t)
	store := dloadLiveStore(t)
	cfg := &Config{}
	cfg.Dload.Interval = 1
	cfg.Dload.IntervalTracing = 30
	d, err := newDloadTracing(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	done := make(chan error, 1)
	go func() { done <- d.Start(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("no persisted host trace before timeout")
		case <-ticker.C:
			documents, err := store()
			if err != nil {
				t.Fatal(err)
			}
			if len(documents) == 0 {
				continue
			}
			if len(documents) != 1 {
				t.Fatalf("expected one trace, got %d", len(documents))
			}
			doc := documents[0]
			data := doc.TracerData.(map[string]any)
			if doc.TracerName != "dload" || doc.ContainerID != "" ||
				doc.TracerRunType != types.TracerRunTypeAutotracing ||
				data["nr_uninterruptible"].(float64) < 1 ||
				data["dload_avg"].(float64) <= 0 ||
				!strings.Contains(data["stack"].(string), fmt.Sprintf("Pid: %d\n", pid)) {
				t.Fatalf("unexpected persisted trace: %+v", doc)
			}
			t.Logf("persisted normal host trigger: D=%v, dload=%v, worker=%d",
				data["nr_uninterruptible"], data["dload_avg"], pid)
			return
		}
	}
}
