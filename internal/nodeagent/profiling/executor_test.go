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

package profiling

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/exec"
	"github.com/ccfos/huatuo/internal/nodeagent/operation"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/types"
)

type fakeResultPublisher struct {
	publishCalls int
}

func (p *fakeResultPublisher) Publish(context.Context, string) error {
	p.publishCalls++
	return nil
}

func TestExecutorClearsExpectedSessionWhenProcessStartFails(t *testing.T) {
	stream, err := toolstream.NewServer(filepath.Join(t.TempDir(), "toolstream.sock"))
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}
	process, err := exec.New(exec.Spec{
		Path: filepath.Join(t.TempDir(), "missing-profiler"),
	})
	if err != nil {
		t.Fatalf("exec.New() error = %v", err)
	}
	publisher := &fakeResultPublisher{}
	executor := newExecutor(process, stream, publisher, "job-1")

	if err := executor.Start(t.Context()); err == nil {
		t.Fatal("Start() error = nil")
	}
	if publisher.publishCalls != 0 {
		t.Fatalf("Publish() calls = %d, want 0", publisher.publishCalls)
	}
	if err := stream.ExpectSession(types.ProfilingToolName, "job-1"); err != nil {
		t.Fatalf("ExpectSession() after failed Start error = %v", err)
	}
	stream.CancelSession(types.ProfilingToolName, "job-1")
}

func TestExecutorFinalizeDiscardCancelsSessionWithoutPublishing(t *testing.T) {
	stream, err := toolstream.NewServer(filepath.Join(t.TempDir(), "toolstream.sock"))
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}
	process, err := exec.New(exec.Spec{Path: "/bin/true"})
	if err != nil {
		t.Fatalf("exec.New() error = %v", err)
	}
	publisher := &fakeResultPublisher{}
	executor := newExecutor(process, stream, publisher, "job-1")
	if err := stream.ExpectSession(types.ProfilingToolName, "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}

	if err := executor.Finalize(t.Context(), operation.FinalizeDiscard); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if publisher.publishCalls != 0 {
		t.Fatalf("Publish() calls = %d, want 0", publisher.publishCalls)
	}
	if err := stream.ExpectSession(types.ProfilingToolName, "job-1"); err != nil {
		t.Fatalf("ExpectSession() after Finalize error = %v", err)
	}
	stream.CancelSession(types.ProfilingToolName, "job-1")
}
