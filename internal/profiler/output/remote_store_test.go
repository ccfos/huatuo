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

package output_test

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

	capnp "capnproto.org/go/capnp/v3"
	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"

	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/toolstream"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestRemoteStoreFormatterSendsResult(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "toolstream.sock")
	server, err := toolstream.NewServer(sockPath)
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}
	received := make(chan *types.ProfilingWindow, 1)
	var commits atomic.Int32
	toolstream.Register(server, "irqtracing", func(_ context.Context, request *toolstream.Request[types.ProfilingWindow]) error {
		if request.Commit {
			commits.Add(1)
		}
		if request.Version != "test" {
			t.Errorf("version = %q", request.Version)
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

	formatter, cleanup, err := output.NewFormatter(output.FormatterConfig{Format: output.FormatRemoteStore, Destination: sockPath}, output.TaskIdentity{ToolName: "irqtracing", TaskID: "task-1", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})

	result := &types.ProfilingWindow{Profile: &profilev1.Profile{}, DroppedSamples: map[string]uint64{types.DropReasonCollection: 3}}

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

func TestRemoteStoreFormatterCleanupDoesNotCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, cleanup, err := output.NewFormatter(output.FormatterConfig{Format: output.FormatRemoteStore, Destination: path}, output.TaskIdentity{ToolName: "profiler", TaskID: "task", Version: "1"})
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
	if err := cleanup(); err != nil {
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
		t.Fatalf("cleanup sent a frame or left connection open: %v", err)
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
			if _, _, err := output.NewFormatter(output.FormatterConfig{Format: output.FormatRemoteStore, Destination: path}, output.TaskIdentity{}); err == nil {
				t.Fatal("accepted non-socket remote destination")
			}
		})
	}
}
