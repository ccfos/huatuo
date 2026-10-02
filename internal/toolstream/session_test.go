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

package toolstream

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

func TestDispatchProcessesDataBeforeCompletingExpectedSession(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}
	var (
		handled    bool
		isExpected bool
	)
	Register(server, "profiler", func(session *Session, event struct {
		Value string `json:"value"`
	},
	) error {
		handled = event.Value == "final"
		isExpected = session.IsExpected
		return nil
	})

	server.dispatch(&transport.Session{
		ToolName: "profiler",
		TaskID:   "job-1",
	}, transport.ChunkMsg{
		Data: []byte(`{"value":"final"}`),
		End:  true,
	})
	if err := server.AwaitSession(t.Context(), "profiler", "job-1"); err != nil {
		t.Fatalf("AwaitSession() error = %v", err)
	}
	if !handled || !isExpected {
		t.Fatalf("handler state = (handled=%t, expected=%t)", handled, isExpected)
	}
}

func TestDispatchCompletesErrorEndForExpectedSession(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}

	server.dispatch(&transport.Session{
		ToolName: "profiler",
		TaskID:   "job-1",
	}, transport.ChunkMsg{Err: "finalization failed", End: true})
	err = server.AwaitSession(t.Context(), "profiler", "job-1")
	if err == nil || !strings.Contains(err.Error(), "finalization failed") {
		t.Fatalf("AwaitSession() error = %v", err)
	}
}

func TestDispatchCompletesHandlerErrorEndForExpectedSession(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}
	handlerErr := errors.New("persist final profile")
	Register(server, "profiler", func(*Session, struct{}) error {
		return handlerErr
	})

	server.dispatch(&transport.Session{
		ToolName: "profiler",
		TaskID:   "job-1",
	}, transport.ChunkMsg{Data: []byte(`{}`), End: true})
	err = server.AwaitSession(t.Context(), "profiler", "job-1")
	if !errors.Is(err, handlerErr) {
		t.Fatalf("AwaitSession() error = %v, want handler error", err)
	}
}

func TestAwaitSessionCancellationRemovesExpectation(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := server.AwaitSession(ctx, "profiler", "job-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("AwaitSession() error = %v, want context.Canceled", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() after cancellation error = %v", err)
	}
}
