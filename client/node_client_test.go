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
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
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
		{name: "token whitespace", config: &NodeConfig{BearerToken: "secret token"}},
		{
			name: "negative timeout",
			config: &NodeConfig{
				BearerToken:    "secret",
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

func TestNewNodeAcceptsConfigWithoutBearerToken(t *testing.T) {
	if _, err := NewNode(&NodeConfig{}); err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}
}

func TestStartOperationRejectsNilRequest(t *testing.T) {
	var observed bool
	client, err := NewNode(&NodeConfig{
		BearerToken: "secret",
		Observe: func(string, time.Duration, error) {
			observed = true
		},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	_, err = client.StartOperation(
		t.Context(),
		NodeAddress{HostPort: "node-1:19704"},
		nil,
	)
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("StartOperation() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != NodeErrorCodeInvalidArgument || nodeErr.StatusCode != 0 {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
	if observed {
		t.Fatal("observer called for request rejected before execution")
	}
}

func TestNodeClientRejectsInvalidAddressBeforeExecution(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, *NodeClient) error
	}{
		{
			name: "start operation",
			call: func(ctx context.Context, client *NodeClient) error {
				_, err := client.StartOperation(
					ctx,
					NodeAddress{HostPort: "node-1"},
					&nodeapi.StartOperationRequest{},
				)
				return err
			},
		},
		{
			name: "get operation",
			call: func(ctx context.Context, client *NodeClient) error {
				_, err := client.GetOperation(
					ctx,
					NodeAddress{HostPort: "node-1"},
					"job-1",
				)
				return err
			},
		},
		{
			name: "stop operation",
			call: func(ctx context.Context, client *NodeClient) error {
				_, err := client.StopOperation(
					ctx,
					NodeAddress{HostPort: "node-1"},
					"job-1",
				)
				return err
			},
		},
		{
			name: "fetch container",
			call: func(ctx context.Context, client *NodeClient) error {
				_, err := client.FetchContainer(
					ctx,
					NodeAddress{HostPort: "node-1"},
					nodeTestContainerID,
				)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				requestCount  int
				observerCount int
			)
			client, err := NewNode(&NodeConfig{
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

			err = tt.call(t.Context(), client)
			var nodeErr *NodeError
			if !errors.As(err, &nodeErr) {
				t.Fatalf("Node client error = %v, want *NodeError", err)
			}
			if nodeErr.Code != NodeErrorCodeInvalidArgument || nodeErr.StatusCode != 0 {
				t.Fatalf("Node client error = %+v", nodeErr)
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

func TestStartOperationSendsGeneratedRequestAndAcceptsHTTP202(t *testing.T) {
	var observedName string
	client, err := NewNode(&NodeConfig{
		BearerToken: "node-secret",
		Observe: func(name string, _ time.Duration, err error) {
			if err != nil {
				t.Errorf("observer error = %v", err)
			}
			observedName = name
		},
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodPost || request.URL.String() != "https://node-1:21970/v1/operations" {
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
	got, err := client.StartOperation(
		t.Context(),
		NodeAddress{HostPort: "node-1:21970", Scheme: "https"},
		request,
	)
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

	_, err = client.GetOperation(
		t.Context(),
		NodeAddress{HostPort: "node-1:19704"},
		"job-1",
	)
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("GetOperation() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != nodeapi.ErrorCodeOperationNotFound ||
		nodeErr.StatusCode != http.StatusNotFound {
		t.Fatalf("Node error = %+v", nodeErr)
	}
}

func TestNodeClientObserverReceivesReturnedError(t *testing.T) {
	var observedErr error
	client, err := NewNode(&NodeConfig{
		BearerToken: "node-secret",
		Observe: func(_ string, _ time.Duration, err error) {
			observedErr = err
		},
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nodeJSONResponse(
				http.StatusOK,
				nodeOperationJSON("other-job", "running"),
			), nil
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	_, returnedErr := client.GetOperation(
		t.Context(),
		NodeAddress{HostPort: "node-1:19704"},
		"job-1",
	)
	if returnedErr == nil {
		t.Fatal("GetOperation() error = nil")
	}
	if !errors.Is(observedErr, returnedErr) {
		t.Fatalf("observer error = %v, want returned error %v", observedErr, returnedErr)
	}
}

func TestNodeClientKeepsNoResponseTransportErrorDistinct(t *testing.T) {
	transportErr := errors.New("connection reset before response")
	client, err := NewNode(&NodeConfig{
		BearerToken: "node-secret",
		HTTPClient: &http.Client{Transport: nodeRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		})},
	})
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}

	_, err = client.GetOperation(
		t.Context(),
		NodeAddress{HostPort: "node-1:19704"},
		"job-1",
	)
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
