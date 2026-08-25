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
	"path/filepath"
	"testing"
	"time"

	"huatuo-bamai/internal/nodeagent/command"
	"huatuo-bamai/internal/toolstream"
)

type fakeResultPublisher struct {
	prepareErr    error
	discardErr    error
	prepareCalls  int
	discardCalls  int
	discardCtxErr error
}

func (p *fakeResultPublisher) Prepare(context.Context, string) error {
	p.prepareCalls++
	return p.prepareErr
}

func (*fakeResultPublisher) Publish(context.Context, string) error { return nil }

func (p *fakeResultPublisher) Discard(ctx context.Context, _ string) error {
	p.discardCalls++
	p.discardCtxErr = ctx.Err()
	return p.discardErr
}

func TestExecutorRollsBackResultWhenProcessStartFails(t *testing.T) {
	stream, err := toolstream.NewServer(filepath.Join(t.TempDir(), "toolstream.sock"))
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}
	process, err := command.New(command.Spec{
		Path:        filepath.Join(t.TempDir(), "missing-profiler"),
		OutputLimit: 1024,
	})
	if err != nil {
		t.Fatalf("command.New() error = %v", err)
	}
	publisher := &fakeResultPublisher{}
	executor := newExecutor(process, stream, publisher, "job-1", time.Second)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := executor.Start(ctx); err == nil {
		t.Fatal("Start() error = nil")
	}
	if publisher.prepareCalls != 1 || publisher.discardCalls != 1 {
		t.Fatalf(
			"publisher calls = (prepare=%d, discard=%d), want (1, 1)",
			publisher.prepareCalls,
			publisher.discardCalls,
		)
	}
	if publisher.discardCtxErr != nil {
		t.Fatalf("Discard() context error = %v, want nil", publisher.discardCtxErr)
	}
	if err := stream.ExpectSession(profilerToolName, "job-1"); err != nil {
		t.Fatalf("ExpectSession() after rollback error = %v", err)
	}
	stream.CancelSession(profilerToolName, "job-1")
}

func TestExecutorReturnsStartAndRollbackFailures(t *testing.T) {
	prepareErr := errors.New("prepare failed")
	discardErr := errors.New("discard failed")
	stream, err := toolstream.NewServer(filepath.Join(t.TempDir(), "toolstream.sock"))
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}
	process, err := command.New(command.Spec{Path: "/bin/true", OutputLimit: 1024})
	if err != nil {
		t.Fatalf("command.New() error = %v", err)
	}
	publisher := &fakeResultPublisher{prepareErr: prepareErr, discardErr: discardErr}
	executor := newExecutor(process, stream, publisher, "job-1", time.Second)

	err = executor.Start(t.Context())
	if !errors.Is(err, prepareErr) || !errors.Is(err, discardErr) {
		t.Fatalf("Start() error = %v, want joined prepare and discard errors", err)
	}
}
