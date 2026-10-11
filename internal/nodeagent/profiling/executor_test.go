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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/executil"
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
	process, err := executil.New(executil.Spec{
		Path: filepath.Join(t.TempDir(), "missing-profiler"),
	})
	if err != nil {
		t.Fatalf("executil.New() error = %v", err)
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
	process, err := executil.New(executil.Spec{Path: "/bin/true"})
	if err != nil {
		t.Fatalf("executil.New() error = %v", err)
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

func TestExecutorFinalizeAlwaysClosesProcess(t *testing.T) {
	tests := []struct {
		name    string
		mode    operation.FinalizeMode
		cancel  bool
		wantErr bool
	}{
		{"discard", operation.FinalizeDiscard, false, false},
		{"canceled finalization", operation.FinalizePublish, true, true},
		{"stream failure", operation.FinalizePublish, false, true},
		{"invalid mode", operation.FinalizeMode(255), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream, err := toolstream.NewServer(filepath.Join(t.TempDir(), "toolstream.sock"))
			if err != nil {
				t.Fatal(err)
			}
			process, err := executil.New(executil.Spec{Path: "/bin/true"}, executil.WithMemfdOutput(64, func(string) []string { return nil }))
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			publisher := &fakeResultPublisher{}
			executor := newExecutor(process, stream, publisher, "job-1")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.cancel {
				cancel()
			}
			err = executor.Finalize(ctx, tt.mode)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Finalize(): %v", err)
			}
			if _, err := process.MemfdOutput(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("MemfdOutput(): %v", err)
			}
			if publisher.publishCalls != 0 {
				t.Fatal("unexpected publication")
			}
		})
	}
}

func TestExecutorClosesProcessWhenSessionRegistrationFails(t *testing.T) {
	process, err := executil.New(executil.Spec{Path: "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	executor := newExecutor(process, nil, &fakeResultPublisher{}, "job-1")
	if err := executor.Start(t.Context()); !errors.Is(err, toolstream.ErrNotInitialized) {
		t.Fatalf("Start(): %v", err)
	}
	if err := process.Start(t.Context()); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("process was not closed: %v", err)
	}
}
