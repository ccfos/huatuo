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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/internal/nodeagent/operation"
	"github.com/ccfos/huatuo/internal/toolstream"
	profilingapi "github.com/ccfos/huatuo/pkg/profiling"
)

func TestExecutorClosesProcessWhenStartFails(t *testing.T) {
	process, err := executil.New(executil.Spec{Path: filepath.Join(t.TempDir(), "missing-profiler")}, executil.WithMemfdOutput(64, func(string) []string { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	executor := newExecutor(process)
	if err := executor.Start(t.Context()); err == nil {
		t.Fatal("Start() succeeded")
	}
	if _, err := process.MemfdOutput(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("process was not closed: %v", err)
	}
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
		{"published", operation.FinalizePublish, false, false},
		{"invalid mode", operation.FinalizeMode(255), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			process, err := executil.New(executil.Spec{Path: "/bin/true"}, executil.WithMemfdOutput(64, func(string) []string { return nil }))
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := process.Wait(); err != nil {
				t.Fatal(err)
			}
			executor := newExecutor(process)
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
		})
	}
}

type readySignalWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *readySignalWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(data), nil
}

func TestExecutorStopAndFinalizeKeepExecutionSeparate(t *testing.T) {
	for _, trap := range []string{"", "trap 'exit 7' TERM; "} {
		t.Run(trap, func(t *testing.T) {
			ready := &readySignalWriter{ready: make(chan struct{})}
			process, err := executil.New(executil.Spec{
				Path: "/bin/sh", Args: []string{"-c", trap + "printf ready; while :; do :; done"},
				StopGracePeriod: time.Second,
			}, executil.WithStdout(ready))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := process.Close(); err != nil {
					t.Error(err)
				}
			})
			executor := newExecutor(process)
			if err := executor.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ready.ready:
			case <-time.After(5 * time.Second):
				t.Fatal("profiler did not become ready")
			}
			if err := executor.Stop(t.Context()); err != nil {
				t.Fatalf("operation Stop reported an execution result as cleanup failure: %v", err)
			}
			waitErr := executor.Wait()
			if trap == "" {
				if !errors.Is(waitErr, operation.ErrStopped) {
					t.Fatalf("Wait() = %v, want operation.ErrStopped", waitErr)
				}
			} else {
				var exitErr *exec.ExitError
				if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 7 {
					t.Fatalf("Wait() = %v, want exit 7", waitErr)
				}
			}
			if err := executor.Finalize(t.Context(), operation.FinalizeDiscard); err != nil {
				t.Fatalf("Finalize repeated execution failure: %v", err)
			}
		})
	}
}

func TestExecutorCommitClient(t *testing.T) {
	socket := os.Getenv("HUATUO_TEST_COMMIT_SOCKET")
	if socket == "" {
		return
	}
	client, err := toolstream.NewClient(&toolstream.ClientOptions{ServiceName: profilingapi.StoreServiceName, SockPath: socket, ToolName: "profiler", Version: "test", TaskID: "job"})
	if err == nil {
		defer client.Close()
		err = client.Send(t.Context(), nil, toolstream.SendOptions{End: true, Commit: true})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func TestExecutorWaitIncludesCommitResult(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "commit.sock")
			server, err := toolstream.NewServer(socket)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			toolstream.Register(server, profilingapi.StoreServiceName, func(_ context.Context, request *toolstream.Request[int]) error {
				if !request.Commit {
					return nil
				}
				close(entered)
				<-release
				if fail {
					return errors.New("publication unavailable")
				}
				return nil
			})
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			t.Cleanup(unblock)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			process, err := executil.New(executil.Spec{
				Path: os.Args[0], Args: []string{"-test.run=^TestExecutorCommitClient$"},
				Env:            append(os.Environ(), "HUATUO_TEST_COMMIT_SOCKET="+socket),
				MaxStderrBytes: 1024,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = process.Close() })
			executor := newExecutor(process)
			if err := executor.Start(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-process.Done():
				t.Fatalf("process exited before commit: %v", executor.Wait())
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-process.Done():
				t.Fatal("process exited before commit completed")
			default:
			}
			unblock()
			err = executor.Wait()
			if (err != nil) != fail {
				t.Fatalf("Wait(): %v", err)
			}
			if fail && !strings.Contains(err.Error(), "publication unavailable") {
				t.Fatalf("Wait() lost commit error: %v", err)
			}
			if err := executor.Finalize(ctx, operation.FinalizePublish); err != nil {
				t.Fatal(err)
			}
		})
	}
}
