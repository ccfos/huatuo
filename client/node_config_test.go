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

package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/ccfos/huatuo/apis/v1"
	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

func TestUpdateConfigSendsGeneratedRequest(t *testing.T) {
	var (
		requestCount int
		observedName string
		observedErr  error
	)
	client, err := NewNode(&NodeConfig{
		BearerToken:    "node-secret",
		RequestTimeout: time.Minute,
		Observe: func(name string, _ time.Duration, err error) {
			observedName = name
			observedErr = err
		},
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			requestCount++
			if request.Method != http.MethodPut || request.URL.String() != "http://node-1:21970/v1/config" {
				t.Fatalf("request = %s %s", request.Method, request.URL)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer node-secret" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := request.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q", got)
			}
			if _, ok := request.Context().Deadline(); !ok {
				t.Fatal("request context has no deadline")
			}
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Fatalf("read request body: %v", readErr)
			}
			var decoded nodeapi.UpdateConfigRequest
			if decodeErr := json.Unmarshal(body, &decoded); decodeErr != nil {
				t.Fatalf("decode request body: %v", decodeErr)
			}
			if got := string(decoded.Config["Runtime.CPULimitCores"]); got != "1.5" {
				t.Fatalf("Runtime.CPULimitCores = %s", got)
			}
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Header:     make(http.Header),
				Body:       http.NoBody,
			}, nil
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	err = client.UpdateConfig(t.Context(), NodeAddress{HostPort: "node-1:21970"}, &nodeapi.UpdateConfigRequest{
		Config: map[string]json.RawMessage{
			"Runtime.CPULimitCores": json.RawMessage("1.5"),
		},
	})
	if err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
	if observedName != "config.update" || observedErr != nil {
		t.Fatalf("observer = (%q, %v)", observedName, observedErr)
	}
}

func TestUpdateConfigRejectsInvalidRequestBeforeExecution(t *testing.T) {
	tests := []struct {
		name    string
		request *nodeapi.UpdateConfigRequest
		wantKey string
	}{
		{name: "nil request"},
		{name: "nil config", request: &nodeapi.UpdateConfigRequest{}},
		{
			name: "empty config",
			request: &nodeapi.UpdateConfigRequest{
				Config: map[string]json.RawMessage{},
			},
		},
		{
			name: "invalid config value",
			request: &nodeapi.UpdateConfigRequest{
				Config: map[string]json.RawMessage{
					"Runtime.CPULimitCores": json.RawMessage("1.5,"),
				},
			},
			wantKey: "Runtime.CPULimitCores",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				requestCount  int
				observerCount int
			)
			client, err := NewNode(&NodeConfig{
				BearerToken: "node-secret",
				Observe: func(string, time.Duration, error) {
					observerCount++
				},
				HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(*http.Request) (*http.Response, error) {
					requestCount++
					return nil, errors.New("unexpected request")
				})},
			})
			if err != nil {
				t.Fatalf("NewNode() error = %v", err)
			}

			err = client.UpdateConfig(
				t.Context(),
				NodeAddress{HostPort: "node-1:19704"},
				tt.request,
			)
			var nodeErr *NodeError
			if !errors.As(err, &nodeErr) {
				t.Fatalf("UpdateConfig() error = %v, want *NodeError", err)
			}
			if nodeErr.Code != NodeErrorCodeInvalidArgument || nodeErr.StatusCode != 0 {
				t.Fatalf("Node client error = %+v", nodeErr)
			}
			if tt.wantKey != "" && !strings.Contains(nodeErr.Message, tt.wantKey) {
				t.Fatalf("Node client error message = %q, want key %q", nodeErr.Message, tt.wantKey)
			}
			if requestCount != 0 || observerCount != 0 {
				t.Fatalf(
					"request count = %d, observer count = %d, want both zero",
					requestCount,
					observerCount,
				)
			}
		})
	}
}

func TestUpdateConfigPreservesTransportError(t *testing.T) {
	transportErr := errors.New("connection reset before response")
	var observedErr error
	client, err := NewNode(&NodeConfig{
		BearerToken: "node-secret",
		Observe: func(_ string, _ time.Duration, err error) {
			observedErr = err
		},
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	err = client.UpdateConfig(
		t.Context(),
		NodeAddress{HostPort: "node-1:19704"},
		testNodeConfigUpdate(),
	)
	if !errors.Is(err, transportErr) {
		t.Fatalf("UpdateConfig() error = %v, want transport error", err)
	}
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) || nodeErr.Code != NodeErrorCodeTransport {
		t.Fatalf("UpdateConfig() error = %v, want transport NodeError", err)
	}
	if !errors.Is(observedErr, err) {
		t.Fatalf("observer error = %v, want returned error %v", observedErr, err)
	}
}

func TestUpdateConfigReturnsAPIError(t *testing.T) {
	client, err := NewNode(&NodeConfig{
		BearerToken: "node-secret",
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nodeJSONResponse(
				http.StatusBadRequest,
				`{"error":{"code":"invalid_request","message":"invalid config"}}`,
			), nil
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	err = client.UpdateConfig(
		t.Context(),
		NodeAddress{HostPort: "node-1:19704"},
		testNodeConfigUpdate(),
	)
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("UpdateConfig() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != apiv1.ErrorCodeInvalidRequest || nodeErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("Node error = %+v", nodeErr)
	}
}

func TestParseNodeConfigResponseRejectsProtocolViolations(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "unexpected success status",
			statusCode: http.StatusOK,
		},
		{
			name:       "no content response with body",
			statusCode: http.StatusNoContent,
			body:       `{}`,
		},
		{
			name:       "oversized error response",
			statusCode: http.StatusBadRequest,
			body:       strings.Repeat("x", maxNodeErrorBodyBytes+1),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := nodeJSONResponse(tt.statusCode, tt.body)
			defer response.Body.Close()
			err := parseNodeConfigResponse(response)
			var nodeErr *NodeError
			if !errors.As(err, &nodeErr) {
				t.Fatalf("parseNodeConfigResponse() error = %v, want *NodeError", err)
			}
			if nodeErr.Code != NodeErrorCodeProtocol || nodeErr.StatusCode != tt.statusCode {
				t.Fatalf("Node client error = %+v", nodeErr)
			}
		})
	}
}

func testNodeConfigUpdate() *nodeapi.UpdateConfigRequest {
	return &nodeapi.UpdateConfigRequest{
		Config: map[string]json.RawMessage{
			"Runtime.CPULimitCores": json.RawMessage("1.5"),
		},
	}
}
