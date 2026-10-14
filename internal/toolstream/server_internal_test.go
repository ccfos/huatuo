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
	"testing"

	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

func TestRequestsContinueAfterFrameFailures(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/requests.sock")
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("first handler failed")
	var requests []*Request[int]
	Register(server, "test", func(_ context.Context, request *Request[int]) error {
		requests = append(requests, request)
		if len(requests) == 1 {
			return failure
		}
		return nil
	})
	session := &transport.Session{Context: t.Context(), ToolName: "test", TaskID: "task", Version: "v1"}
	server.dispatch(session, transport.ChunkMsg{Data: []byte("1")})
	server.dispatch(session, transport.ChunkMsg{Data: []byte("bad json")})
	server.dispatch(session, transport.ChunkMsg{Data: []byte("2"), End: true, Commit: true})
	if len(requests) != 3 {
		t.Fatalf("handler received %d frames", len(requests))
	}
	if requests[0].PriorErr != nil || requests[0].Err != nil {
		t.Fatal("first frame inherited later errors")
	}
	if !errors.Is(requests[1].PriorErr, failure) || requests[1].Err == nil || requests[1].Data != nil {
		t.Fatalf("invalid decoding request: %+v", requests[1])
	}
	last := requests[2]
	if last.Data == nil || *last.Data != 2 || !last.Commit || !last.End || last.Err != nil || !errors.Is(last.PriorErr, failure) {
		t.Fatalf("final frame not delivered with prior error: %+v", last)
	}
	if last.ToolName != "test" || last.TaskID != "task" || last.Version != "v1" {
		t.Fatalf("metadata=%+v", last)
	}
	if !errors.Is(session.Err, failure) {
		t.Fatalf("session error lost: %v", session.Err)
	}
}

func TestRequestDecodeFailureDiscardsPartialData(t *testing.T) {
	for _, payload := range []string{`null`, `{"Value":1,"Count":"invalid"}`} {
		t.Run(payload, func(t *testing.T) {
			server, err := NewServer(t.TempDir() + "/requests.sock")
			if err != nil {
				t.Fatal(err)
			}

			type event struct {
				Value int
				Count int
			}
			called := false
			Register(server, "test", func(_ context.Context, request *Request[event]) error {
				called = true
				if request.Data != nil || request.Err == nil {
					t.Fatalf("decode failure request: %+v", request)
				}
				// A handler cannot erase the error acknowledged to the producer.
				request.Err = nil
				return nil
			})

			session := &transport.Session{Context: t.Context(), ToolName: "test"}
			server.dispatch(session, transport.ChunkMsg{Data: []byte(payload), End: true, Commit: true})
			if !called || session.Err == nil {
				t.Fatalf("called=%v, session error=%v", called, session.Err)
			}
		})
	}
}
