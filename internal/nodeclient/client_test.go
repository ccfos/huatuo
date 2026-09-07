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

package nodeclient

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

type closeErrorBody struct {
	io.Reader
	err error
}

func (b closeErrorBody) Close() error { return b.err }

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func operationJSON(requestID, status string) string {
	if status == "completed" || status == "failed" || status == "stopped" {
		return fmt.Sprintf(
			`{"data":{"created_at":"2026-08-24T12:00:00Z",`+
				`"request_id":%q,"kind":"profiling","status":"terminal",`+
				`"terminal":{"outcome":%q}}}`,
			requestID,
			status,
		)
	}
	return fmt.Sprintf(
		`{"data":{"created_at":"2026-08-24T12:00:00Z",`+
			`"request_id":%q,"kind":"profiling","status":%q}}`,
		requestID,
		status,
	)
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
	}{
		{name: "nil config"},
		{name: "empty token", config: &Config{}},
		{name: "token whitespace", config: &Config{BearerToken: "secret token"}},
		{name: "missing port", config: &Config{BearerToken: "secret"}},
		{name: "invalid port", config: &Config{BearerToken: "secret", Port: 70000}},
		{
			name: "negative timeout",
			config: &Config{
				BearerToken:    "secret",
				Port:           19704,
				RequestTimeout: -time.Second,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.config); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestStartOperationRejectsNilRequest(t *testing.T) {
	client, err := New(&Config{BearerToken: "secret", Port: 19704})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = client.StartOperation(t.Context(), "node-1", nil)
	var nodeErr *Error
	if !errors.As(err, &nodeErr) {
		t.Fatalf("StartOperation() error = %v, want *Error", err)
	}
	if nodeErr.Code != ErrorCodeClientInvalidArgument || nodeErr.StatusCode != 0 {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
}

func TestStartOperationSendsGeneratedRequestAndAcceptsHTTP202(t *testing.T) {
	var observedName string
	client, err := New(&Config{
		BearerToken: "node-secret",
		Port:        21970,
		Observe: func(name string, _ time.Duration, err error) {
			if err != nil {
				t.Errorf("observer error = %v", err)
			}
			observedName = name
		},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
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
			return jsonResponse(http.StatusAccepted, operationJSON("job-1", "pending")), nil
		})},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
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
	client, err := New(&Config{
		BearerToken: "node-secret",
		Port:        19704,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(
				http.StatusNotFound,
				`{"error":{"code":"operation_not_found","message":"operation not found"}}`,
			), nil
		})},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = client.GetOperation(t.Context(), "node-1", "job-1")
	var nodeErr *Error
	if !errors.As(err, &nodeErr) {
		t.Fatalf("GetOperation() error = %v, want *Error", err)
	}
	if nodeErr.Code != nodeapi.ErrorCodeOperationNotFound ||
		nodeErr.StatusCode != http.StatusNotFound {
		t.Fatalf("Node error = %+v", nodeErr)
	}
}

func TestNodeClientKeepsNoResponseTransportErrorDistinct(t *testing.T) {
	transportErr := errors.New("connection reset before response")
	client, err := New(&Config{
		BearerToken: "node-secret",
		Port:        19704,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, transportErr
		})},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = client.GetOperation(t.Context(), "node-1", "job-1")
	if !errors.Is(err, transportErr) {
		t.Fatalf("GetOperation() error = %v, want transport error", err)
	}
	var nodeErr *Error
	if !errors.As(err, &nodeErr) {
		t.Fatalf("GetOperation() error = %v, want *Error", err)
	}
	if nodeErr.Code != ErrorCodeClientTransport || nodeErr.StatusCode != 0 {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
}

func TestParseResponseRejectsProtocolViolations(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		successMode successResponseMode
	}{
		{
			name:        "accepted get response",
			statusCode:  http.StatusAccepted,
			body:        operationJSON("job-1", "pending"),
			successMode: successResponseOK,
		},
		{
			name:       "mismatched response ID",
			statusCode: http.StatusOK,
			body:       operationJSON("other-job", "running"),
		},
		{
			name:       "status and error code mismatch",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":{"code":"operation_not_found","message":"missing"}}`,
		},
		{
			name:       "client error code in server response",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":{"code":"client_transport","message":"unavailable"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := jsonResponse(tt.statusCode, tt.body)
			defer response.Body.Close()
			_, err := parseResponse(response, "job-1", tt.successMode)
			var nodeErr *Error
			if !errors.As(err, &nodeErr) {
				t.Fatalf("parseResponse() error = %v, want *Error", err)
			}
			if nodeErr.Code != ErrorCodeClientProtocol || nodeErr.StatusCode != tt.statusCode {
				t.Fatalf("Node client error = %+v", nodeErr)
			}
		})
	}
}

func TestParseResponseUsesBodyInsteadOfContentType(t *testing.T) {
	for _, test := range []struct {
		name        string
		contentType string
	}{
		{name: "missing", contentType: ""},
		{name: "inaccurate", contentType: "text/plain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := jsonResponse(http.StatusOK, operationJSON("job-1", "pending"))
			response.Header.Set("Content-Type", test.contentType)

			operation, err := parseResponse(response, "job-1", successResponseOK)
			if err != nil {
				t.Fatalf("parseResponse() error = %v", err)
			}
			if operation.RequestID != "job-1" {
				t.Fatalf("parseResponse() request ID = %q, want %q", operation.RequestID, "job-1")
			}
		})
	}
}

func TestParseResponseClassifiesBodyCloseFailureAsTransportError(t *testing.T) {
	closeErr := errors.New("close response body")
	response := jsonResponse(http.StatusOK, operationJSON("job-1", "completed"))
	response.Body = closeErrorBody{
		Reader: response.Body,
		err:    closeErr,
	}

	_, err := parseResponse(response, "job-1", successResponseOK)
	var nodeErr *Error
	if !errors.As(err, &nodeErr) {
		t.Fatalf("parseResponse() error = %v, want *Error", err)
	}
	if nodeErr.Code != ErrorCodeClientTransport || nodeErr.StatusCode != http.StatusOK {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("parseResponse() error = %v, want close error", err)
	}
}
