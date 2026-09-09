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
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
)

type nodeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f nodeRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestNewNodeRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *NodeConfig
	}{
		{name: "nil config"},
		{name: "empty token", config: &NodeConfig{}},
		{name: "token whitespace", config: &NodeConfig{BearerToken: "secret token"}},
		{name: "missing port", config: &NodeConfig{BearerToken: "secret"}},
		{name: "invalid port", config: &NodeConfig{BearerToken: "secret", Port: 70000}},
		{
			name: "negative timeout",
			config: &NodeConfig{
				BearerToken:    "secret",
				Port:           19704,
				RequestTimeout: -time.Second,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewNode(tt.config); err == nil {
				t.Fatal("NewNode() error = nil")
			}
		})
	}
}

func TestStartOperationRejectsNilRequest(t *testing.T) {
	client, err := NewNode(&NodeConfig{BearerToken: "secret", Port: 19704})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	_, err = client.StartOperation(t.Context(), "node-1", nil)
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("StartOperation() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != NodeErrorCodeInvalidArgument || nodeErr.StatusCode != 0 {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
}

func TestStartOperationSendsGeneratedRequestAndAcceptsHTTP202(t *testing.T) {
	var observedName string
	client, err := NewNode(&NodeConfig{
		BearerToken: "node-secret",
		Port:        21970,
		Observe: func(name string, _ time.Duration, err error) {
			if err != nil {
				t.Errorf("observer error = %v", err)
			}
			observedName = name
		},
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodPost || request.URL.String() != "http://node-1:21970/v1/operations" {
				t.Fatalf("request = %s %s", request.Method, request.URL)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer node-secret" {
				t.Fatalf("Authorization = %q", got)
			}
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Fatalf("read request body: %v", readErr)
			}
			if !strings.Contains(string(body), `"request_id":"job-1"`) {
				t.Fatalf("request body = %s", body)
			}
			return nodeJSONResponse(http.StatusAccepted, nodeOperationJSON("job-1", "pending")), nil
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	request := &nodeapi.StartOperationRequest{
		RequestID:       "job-1",
		DurationSeconds: 60,
		Scope:           "host",
		Kind:            nodeapi.OperationKindProfiling,
	}
	if err := request.Spec.FromProfilingOperationSpec(nodeapi.ProfilingOperationSpec{
		Type: "cpu", Language: "go", Mode: "oncpu",
	}); err != nil {
		t.Fatalf("set profiling spec: %v", err)
	}
	got, err := client.StartOperation(t.Context(), "node-1", request)
	if err != nil {
		t.Fatalf("StartOperation() error = %v", err)
	}
	if got.RequestID != "job-1" || got.Status != nodeapi.OperationStatusPending {
		t.Fatalf("StartOperation() = %+v", got)
	}
	if observedName != "operation.start" {
		t.Fatalf("observed operation = %q", observedName)
	}
}

func TestGetOperationReturnsStableNodeError(t *testing.T) {
	client, err := NewNode(&NodeConfig{
		BearerToken: "node-secret",
		Port:        19704,
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nodeJSONResponse(
				http.StatusNotFound,
				`{"error":{"code":"operation_not_found","message":"operation not found"}}`,
			), nil
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	_, err = client.GetOperation(t.Context(), "node-1", "job-1")
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("GetOperation() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != nodeapi.ErrorCodeOperationNotFound ||
		nodeErr.StatusCode != http.StatusNotFound {
		t.Fatalf("Node error = %+v", nodeErr)
	}
}

func TestNodeClientKeepsNoResponseTransportErrorDistinct(t *testing.T) {
	transportErr := errors.New("connection reset before response")
	client, err := NewNode(&NodeConfig{
		BearerToken: "node-secret",
		Port:        19704,
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	_, err = client.GetOperation(t.Context(), "node-1", "job-1")
	if !errors.Is(err, transportErr) {
		t.Fatalf("GetOperation() error = %v, want transport error", err)
	}
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("GetOperation() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != NodeErrorCodeTransport || nodeErr.StatusCode != 0 {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
}
