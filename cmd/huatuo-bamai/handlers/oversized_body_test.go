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

package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/pkg/tracing"
)

const taskBodyLimit = int64(96)

func startTaskBodyLimitServer(t *testing.T) (string, *server.Server) {
	t.Helper()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release reserved address: %v", err)
	}

	srv := server.NewServer(&server.Config{MaxBodyBytes: taskBodyLimit})
	srv.MustRegisterRoutes("/tasks", NewTaskHandler().Handlers)
	if err := srv.Start(address); err != nil {
		t.Fatalf("Start(%q) error=%v", address, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error=%v", err)
		}
	})
	return "http://" + address, srv
}

func oversizedTaskBody(requestID string) string {
	return `{"request_id":"` + requestID + `","tracer_name":"cpu","timeout":1,` +
		`"data_type":"db","trace_args":["` + strings.Repeat("x", 256) + `"]}`
}

func requireAPIError(
	t *testing.T,
	resp *http.Response,
	wantStatus int,
	wantMessage string,
) {
	t.Helper()
	defer resp.Body.Close()

	var body v1.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("status=%d, want %d; body=%+v", resp.StatusCode, wantStatus, body)
	}
	if body.Error.Code != v1.ErrorCodeInvalidRequest {
		t.Fatalf("error code=%q, want %q", body.Error.Code, v1.ErrorCodeInvalidRequest)
	}
	if body.Error.Message != wantMessage {
		t.Fatalf("error message=%q, want %q", body.Error.Message, wantMessage)
	}
}

func TestTaskHandlerReportsKnownLengthBodyLimit(t *testing.T) {
	baseURL, _ := startTaskBodyLimitServer(t)
	const requestID = "oversized-known"

	resp, err := http.Post(
		baseURL+"/tasks",
		"application/json",
		strings.NewReader(oversizedTaskBody(requestID)),
	)
	if err != nil {
		t.Fatalf("POST /tasks error=%v", err)
	}
	requireAPIError(t, resp, http.StatusRequestEntityTooLarge,
		"request body exceeds 96 bytes")
	if result := tracing.Result(requestID); result.TaskStatus != tracing.StatusNotExist {
		t.Fatalf("task status=%s, want no task creation", result.TaskStatus)
	}
}

func TestTaskHandlerReportsChunkedBodyLimit(t *testing.T) {
	baseURL, _ := startTaskBodyLimitServer(t)
	const requestID = "oversized-chunked"

	req, err := http.NewRequest(
		http.MethodPost,
		baseURL+"/tasks",
		io.NopCloser(strings.NewReader(oversizedTaskBody(requestID))),
	)
	if err != nil {
		t.Fatalf("NewRequest() error=%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chunked POST /tasks error=%v", err)
	}
	requireAPIError(t, resp, http.StatusRequestEntityTooLarge,
		"request body exceeds 96 bytes")
	if result := tracing.Result(requestID); result.TaskStatus != tracing.StatusNotExist {
		t.Fatalf("task status=%s, want no task creation", result.TaskStatus)
	}
}

func TestTaskHandlerKeepsMalformedBodyAsBadRequest(t *testing.T) {
	baseURL, _ := startTaskBodyLimitServer(t)

	resp, err := http.Post(baseURL+"/tasks", "application/json", strings.NewReader(`{"request_id":`))
	if err != nil {
		t.Fatalf("POST malformed /tasks error=%v", err)
	}
	requireAPIError(t, resp, http.StatusBadRequest, "unexpected EOF")
}
