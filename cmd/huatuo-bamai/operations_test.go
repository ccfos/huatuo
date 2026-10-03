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

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/nodeagent/operation"
	nodeprofiling "github.com/ccfos/huatuo/internal/nodeagent/profiling"
	"github.com/ccfos/huatuo/internal/profiling/publication"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/observation"
	"github.com/ccfos/huatuo/pkg/profiling"
)

func TestToResultPublisher(t *testing.T) {
	if publisher := toResultPublisher(nil); publisher != nil {
		t.Fatal("nil publication store produced a non-nil ResultPublisher")
	}

	store := &publication.Store{}
	if publisher := toResultPublisher(store); publisher != store {
		t.Fatal("ResultPublisher does not retain the original publication store")
	}
}

func TestStartOperationsRejectsProfilingWithoutStorage(t *testing.T) {
	const helperEnv = "HUATUO_TEST_PROFILING_WITHOUT_STORAGE"
	if os.Getenv(helperEnv) != "1" {
		// Config loading publishes process-wide state that cannot be reset to uninitialized.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStartOperationsRejectsProfilingWithoutStorage$")
		command.Env = append(os.Environ(), helperEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("storage-disabled profiling test: %v\n%s", err, output)
		}
		return
	}

	if err := config.Load("../../huatuo-bamai.conf"); err != nil {
		t.Fatalf("load bamai config: %v", err)
	}
	directory := t.TempDir()
	stream, err := toolstream.NewServer(filepath.Join(directory, "toolstream.sock"))
	if err != nil {
		t.Fatalf("create toolstream server: %v", err)
	}
	daemon := NewDaemon(&Options{ToolBinDir: directory})
	daemon.toolstreamServer = stream
	shutdown, err := startOperations(daemon)
	if err != nil {
		t.Fatalf("startOperations() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			t.Errorf("shutdown operations: %v", err)
		}
	})
	request := &nodeprofiling.StartRequest{
		RequestID: "without-storage",
		Duration:  time.Second,
		Scope:     observation.ScopeHost,
		Spec: profiling.Spec{
			Type:     profiling.TypeCPU,
			Language: profiling.LanguageGo,
			Mode:     profiling.ModeOnCPU,
		},
	}
	snapshot, created, err := daemon.profilingService.Start(t.Context(), request)
	if !errors.Is(err, nodeprofiling.ErrEnvironmentUnsupported) {
		t.Fatalf("Start() error = %v, want ErrEnvironmentUnsupported", err)
	}
	// Missing storage must be rejected before checking the absent profiler executable.
	if !strings.Contains(err.Error(), "profiling result storage is not configured") {
		t.Errorf("Start() rejected request for an unexpected reason: %v", err)
	}
	if snapshot != nil || created {
		t.Errorf("Start() = (%v, %t), want (nil, false)", snapshot, created)
	}
	if _, err := daemon.operationManager.GetByID(request.RequestID); !errors.Is(err, operation.ErrNotFound) {
		t.Errorf("GetByID() error = %v, want ErrNotFound", err)
	}
}
