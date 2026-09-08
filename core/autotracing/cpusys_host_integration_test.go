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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/tracing"

	internalconfig "github.com/ccfos/huatuo/internal/config"

	_ "github.com/ccfos/huatuo/internal/storage/localfile"

	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
)

func cpuHostTestStore(t *testing.T) func() ([]*tracingstore.Document, error) {
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
		file, err := os.Open(filepath.Join(dir, "cpusys"))
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

func TestCPUHostPersistence(t *testing.T) {
	store := cpuHostTestStore(t)
	c := cpuSysTracing{
		userThreshold:  cpuSysThreshold{usage: 75, delta: 45},
		totalThreshold: cpuSysThreshold{usage: 90, delta: 55},
		threshold:      cpuSysThreshold{usage: 45, delta: 20},
	}
	state := cpuSysState{userPercent: 80, userPercentDelta: 60, totalPercent: 95, totalPercentDelta: 65}
	if err := c.saveCPUSysTrace(time.Now(), &state, []byte("[]")); err != nil {
		t.Fatal(err)
	}
	docs, err := store()
	if err != nil || len(docs) != 1 {
		t.Fatalf("documents=%d error=%v", len(docs), err)
	}
	data := docs[0].TracerData.(map[string]any)
	if docs[0].ContainerID != "" || docs[0].TracerName != "cpusys" ||
		data["user_percent"] != float64(80) || data["total_percent"] != float64(95) ||
		len(data["trigger_reasons"].([]any)) != 2 {
		t.Fatalf("unexpected CPU document: %+v", docs[0])
	}
}

func TestCPUHostLiveTrigger(t *testing.T) {
	dir := os.Getenv("HUATUO_CPU_LIVE_DIR")
	if dir == "" {
		t.Skip("set HUATUO_CPU_LIVE_DIR to a test VM directory with perf and perf.o")
	}
	previousBin, previousBPF := internalconfig.CoreBinDir, internalconfig.CoreBpfDir
	internalconfig.CoreBinDir, internalconfig.CoreBpfDir = dir, dir
	defer func() { internalconfig.CoreBinDir, internalconfig.CoreBpfDir = previousBin, previousBPF }()
	store := cpuHostTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	worker := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 3; while :; do :; done")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = worker.Wait() }()
	c := cpuSysTracing{
		interval: time.Second, minTraceInterval: time.Minute, perfDuration: time.Second,
		userThreshold:  cpuSysThreshold{usage: 1, delta: 1},
		totalThreshold: cpuSysThreshold{usage: 1, delta: 1},
		threshold:      cpuSysThreshold{usage: 100, delta: 100},
	}
	done := make(chan struct{})
	var runErr error
	go func() { runErr = c.Start(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			t.Fatalf("CPU tracer stopped before persistence: %v", runErr)
		case <-ctx.Done():
			t.Fatal("no persisted CPU trigger before timeout")
		case <-ticker.C:
			docs, err := store()
			if err != nil {
				t.Fatal(err)
			}
			if len(docs) == 0 {
				continue
			}
			if len(docs) != 1 {
				t.Fatalf("duplicate CPU captures: %d", len(docs))
			}
			doc := docs[0]
			data := doc.TracerData.(map[string]any)
			reasons := data["trigger_reasons"].([]any)
			if doc.TracerName != "cpusys" || doc.ContainerID != "" ||
				!slices.Contains(reasons, any("user")) || !slices.Contains(reasons, any("total")) ||
				len(data["flamedata"].([]any)) == 0 {
				t.Fatalf("unexpected live CPU trace: %+v", doc)
			}
			t.Logf("real /proc/stat -> user+total trigger -> perf -> local file: user=%v total=%v flame roots=%d",
				data["user_percent"], data["total_percent"], len(data["flamedata"].([]any)))
			return
		}
	}
}
