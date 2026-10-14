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

package toolstream_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/toolstream"
)

func TestCommitAcknowledgesAllWindowsAndPublication(t *testing.T) {
	for _, failure := range []string{"", "window", "publish", "end"} {
		name := failure
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commit.sock")
			server, err := toolstream.NewServer(path)
			if err != nil {
				t.Fatal(err)
			}
			var windows, commits atomic.Int32
			toolstream.Register(server, "test", func(_ context.Context, request *toolstream.Request[int]) error {
				if request.Data != nil {
					n := *request.Data
					windows.Add(1)
					if failure == "window" && n == 1 {
						return errors.New("first window failed")
					}
				}
				if request.End && !request.Commit {
					return errors.New("missing commit")
				}
				if !request.Commit || request.PriorErr != nil || request.Err != nil {
					return nil
				}
				if windows.Load() != 2 {
					return errors.New("commit overtook windows")
				}
				commits.Add(1)
				if failure == "publish" {
					return errors.New("publish failed")
				}
				return nil
			})
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			client, err := toolstream.NewClient(toolstream.ClientOptions{SockPath: path, ToolName: "test", Version: "test", TaskID: "job"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			for _, n := range []int{1, 2} {
				if err := client.Send(t.Context(), n, toolstream.SendOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if failure == "end" {
				if err := client.Send(t.Context(), nil, toolstream.SendOptions{End: true}); err != nil {
					t.Fatal(err)
				}
			} else {
				err = client.Send(ctx, nil, toolstream.SendOptions{End: true, Commit: true})
				if (err != nil) != (failure != "") {
					t.Fatalf("Send(commit): %v", err)
				}
				if failure == "window" && !strings.Contains(err.Error(), "first window failed") {
					t.Fatal(err)
				}
			}
			// End has no ACK; closing the server joins any in-flight callback.
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			want := int32(1)
			if failure == "window" || failure == "end" {
				want = 0
			}
			if commits.Load() != want {
				t.Fatalf("commits=%d want=%d", commits.Load(), want)
			}
		})
	}
}

func TestCommitCancellationDoesNotClaimRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commit.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	toolstream.Register(server, "test", func(_ context.Context, request *toolstream.Request[int]) error {
		if !request.Commit {
			return nil
		}
		close(entered)
		<-release
		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := toolstream.NewClient(toolstream.ClientOptions{SockPath: path, ToolName: "test", Version: "test", TaskID: "job"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.Send(ctx, nil, toolstream.SendOptions{End: true, Commit: true}) }()
	<-entered
	cancel()
	err = <-result
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send(commit): %v", err)
	}
	// Joining the server waits for the callback even when the ACK was lost.
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentToolsAndDuplicateConnection(t *testing.T) {
	for _, failWindow := range []bool{false, true} {
		name := "success"
		if failWindow {
			name = "failed window"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commit.sock")
			server, err := toolstream.NewServer(path)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var profilerCommits, irqCommits atomic.Int32
			toolstream.Register(server, "profiler", func(_ context.Context, request *toolstream.Request[int]) error {
				if request.Data != nil {
					close(entered)
					<-release
					if failWindow {
						return errors.New("window storage failed")
					}
				}
				if request.Commit && request.PriorErr == nil && request.Err == nil {
					profilerCommits.Add(1)
				}
				return nil
			})
			toolstream.Register(server, "irqtracing", func(_ context.Context, request *toolstream.Request[int]) error {
				if !request.Commit {
					return nil
				}
				irqCommits.Add(1)
				return nil
			})
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Close() })
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			connect := func(tool string) *toolstream.Client {
				t.Helper()
				client, err := toolstream.NewClient(toolstream.ClientOptions{SockPath: path, ToolName: tool, Version: "test", TaskID: "shared-id"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = client.Close() })
				return client
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			owner := connect("profiler")
			if err := owner.Send(ctx, 1, toolstream.SendOptions{}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := connect("profiler").Send(ctx, nil, toolstream.SendOptions{End: true, Commit: true}); err == nil || !strings.Contains(err.Error(), "already belongs") {
				t.Fatalf("duplicate Send(commit): %v", err)
			}
			// IRQ must finish while the profiler's storage handler remains blocked.
			if err := connect("irqtracing").Send(ctx, nil, toolstream.SendOptions{End: true, Commit: true}); err != nil {
				t.Fatalf("IRQ Send(commit): %v", err)
			}
			unblock()
			if err := owner.Send(ctx, nil, toolstream.SendOptions{End: true, Commit: true}); (err != nil) != failWindow {
				t.Fatalf("owner Send(commit): %v", err)
			}
			want := int32(1)
			if failWindow {
				want = 0
			}
			if profilerCommits.Load() != want || irqCommits.Load() != 1 {
				t.Fatalf("commits profiler=%d irq=%d", profilerCommits.Load(), irqCommits.Load())
			}
		})
	}
}

func TestSendFinalDataAndCommitInOneFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commit.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	var windows, commits atomic.Int32
	toolstream.Register(server, "test", func(_ context.Context, request *toolstream.Request[int]) error {
		if request.Data != nil {
			n := *request.Data
			if n != 42 {
				return errors.New("unexpected final payload")
			}
			windows.Add(1)
		}
		if !request.Commit {
			return nil
		}
		if windows.Load() != 1 {
			return errors.New("commit overtook final payload")
		}
		commits.Add(1)
		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := toolstream.NewClient(toolstream.ClientOptions{SockPath: path, ToolName: "test", Version: "test", TaskID: "final"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := client.Send(ctx, 42, toolstream.SendOptions{Commit: true}); err == nil {
		t.Fatal("commit without end was accepted")
	}
	if err := client.Send(ctx, 42, toolstream.SendOptions{End: true, Commit: true}); err != nil {
		t.Fatal(err)
	}
	if windows.Load() != 1 || commits.Load() != 1 {
		t.Fatalf("windows=%d commits=%d", windows.Load(), commits.Load())
	}
}
