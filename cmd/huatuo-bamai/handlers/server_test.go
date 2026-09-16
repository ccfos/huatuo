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
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/pod"
)

func TestNodeRouterTokenAuthentication(t *testing.T) {
	nodeHandler := newTestNodeAPIHandler(t, time.Second)
	nodeHandler.containerByID = func(string) (*pod.Container, error) { return nil, nil }
	baseURL := startNodeAPIServer(t, nodeHandler)

	tests := []struct {
		name       string
		path       string
		token      string
		wantStatus int
	}{
		{name: "public readiness", path: "/readyz", wantStatus: http.StatusNoContent},
		{
			name:       "public container metadata",
			path:       "/v1/containers/" + testContainerID,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing token",
			path:       "/v1/operations/job-1",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid token",
			path:       "/v1/operations/job-1",
			token:      "other-secret",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "valid token",
			path:       "/v1/operations/job-1",
			token:      "node-secret",
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := nodeRequestStatus(t, baseURL+tt.path, tt.token)
			if status != tt.wantStatus {
				t.Fatalf("GET %s status = %d, want %d", tt.path, status, tt.wantStatus)
			}
		})
	}
}

func startNodeAPIServer(t *testing.T, nodeHandler *NodeAPIHandler) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close() listener error = %v", err)
	}

	server, err := newHTTPServer(&ServerOptions{BearerToken: "node-secret"}, nodeHandler)
	if err != nil {
		t.Fatalf("newHTTPServer() error = %v", err)
	}
	if err := server.Start(addr); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	})
	return "http://" + addr
}

func nodeRequestStatus(t *testing.T, url, token string) int {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s error = %v", url, err)
	}
	defer response.Body.Close()
	return response.StatusCode
}
