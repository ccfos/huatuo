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

package remotestore_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiling/formatter/remotestore"

	capnp "capnproto.org/go/capnp/v3"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/internal/toolstream"
	profilingapi "github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestRemoteStoreFormatterSendsResult(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "toolstream.sock")
	server, err := toolstream.NewServer(sockPath)
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}

	received := make(chan *types.AggregationWindow, 1)
	var commits atomic.Int32
	toolstream.Register(server, profilingapi.StoreServiceName, func(
		_ context.Context,
		request *toolstream.Request[types.AggregationWindow],
	) error {
		if request.Commit {
			commits.Add(1)
		}

		if request.Version != "3" {
			t.Errorf("version = %q", request.Version)
		}

		if request.ServiceName != profilingapi.StoreServiceName || request.ToolName != "irqtracing" {
			t.Errorf("service=%q tool=%q", request.ServiceName, request.ToolName)
		}

		if request.Data == nil {
			return nil
		}

		result := request.Data
		if request.TaskID != "task-1" {
			t.Errorf("TaskID = %q, want task-1", request.TaskID)
		}

		received <- result

		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatalf("server.Start() error = %v", err)
	}

	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Errorf("server.Close() error = %v", err)
		}
	})

	formatter, err := remotestore.NewFormatter(output.Config{
		Format:      output.FormatRemoteStore,
		Destination: sockPath,
		TaskID:      "task-1",
		ToolName:    "irqtracing",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := formatter.Close(); err != nil {
			t.Error(err)
		}
	})

	result := &types.AggregationWindow{
		ProfileType:    "irqtracing:irq:count:irq:count",
		DroppedSamples: map[string]uint64{types.DropReasonCollection: 3},
	}

	if err := formatter.Add(t.Context(), result); err != nil {
		t.Fatal(err)
	}

	if err := formatter.Complete(t.Context()); err != nil {
		t.Fatalf("send end marker: %v", err)
	}

	if commits.Load() != 1 {
		t.Fatalf("commits = %d, want 1", commits.Load())
	}

	select {
	case got := <-received:
		if got.DroppedSamples[types.DropReasonCollection] != 3 {
			t.Fatalf("received result = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Toolstream result")
	}
}

func TestRemoteStoreFormatterCloseDoesNotCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}

	defer listener.Close()
	formatter, err := remotestore.NewFormatter(output.Config{
		Format:      output.FormatRemoteStore,
		Destination: path,
		TaskID:      "task",
		ToolName:    "profiler",
	})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	if err := formatter.Close(); err != nil {
		t.Fatal(err)
	}

	decoder := capnp.NewDecoder(conn)
	message, err := decoder.Decode()
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}

	message.Release()
	if message, err := decoder.Decode(); !errors.Is(err, io.EOF) {
		if message != nil {
			message.Release()
		}

		t.Fatalf("Close sent a frame or left connection open: %v", err)
	}
}

func TestRemoteStoreFormatterRejectsNonSocket(t *testing.T) {
	for _, name := range []string{"missing", "file", "directory"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "destination")
			switch name {
			case "file":
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := remotestore.NewFormatter(output.Config{
				Format:      output.FormatRemoteStore,
				Destination: path,
			}); err == nil {
				t.Fatal("accepted non-socket remote destination")
			}
		})
	}
}

func TestRemoteStoreFormatterReturnsCommitFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.sock")
	server, err := toolstream.NewServer(path)
	if err != nil {
		t.Fatal(err)
	}

	var commits atomic.Int32
	toolstream.Register(server, profilingapi.StoreServiceName, func(
		_ context.Context,
		request *toolstream.Request[types.AggregationWindow],
	) error {
		if request.Commit {
			commits.Add(1)
			return errors.New("store commit rejected")
		}

		return nil
	})
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	f, err := remotestore.NewFormatter(output.Config{
		Format:      output.FormatRemoteStore,
		Destination: path,
		TaskID:      "failed-commit",
		ToolName:    "profiler",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	first := f.Complete(t.Context())
	if first == nil {
		t.Fatal("commit rejection was ignored")
	}

	if commits.Load() != 1 {
		t.Fatalf("commit attempts = %d", commits.Load())
	}
}
