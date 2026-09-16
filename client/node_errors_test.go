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
	"net/http"
	"testing"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

func TestNodeErrorError(t *testing.T) {
	tests := []struct {
		name     string
		nodeErr  *NodeError
		expected string
	}{
		{
			name:     "nil receiver",
			expected: "node client error",
		},
		{
			name: "client error without response",
			nodeErr: &NodeError{
				Code:    NodeErrorCodeInvalidArgument,
				Message: "operation request is required",
			},
			expected: "node client client_invalid_argument: operation request is required",
		},
		{
			name: "client error with response",
			nodeErr: &NodeError{
				StatusCode: http.StatusOK,
				Code:       NodeErrorCodeProtocol,
				Message:    "invalid response",
			},
			expected: "node client client_protocol for HTTP 200: invalid response",
		},
		{
			name: "transport error with response",
			nodeErr: &NodeError{
				StatusCode: http.StatusBadGateway,
				Code:       NodeErrorCodeTransport,
				Message:    "read Node API response",
			},
			expected: "node client client_transport for HTTP 502: read Node API response",
		},
		{
			name: "server error",
			nodeErr: &NodeError{
				StatusCode: http.StatusNotFound,
				Code:       nodeapi.ErrorCodeOperationNotFound,
				Message:    "operation not found",
			},
			expected: "node API returned HTTP 404 operation_not_found: operation not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.nodeErr.Error(); got != tt.expected {
				t.Fatalf("NodeError.Error() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestWrapNodeErrorPreservesBothErrors(t *testing.T) {
	nodeErr := &NodeError{
		Code:    NodeErrorCodeTransport,
		Message: "get Node Operation",
	}
	cause := errors.New("connection reset")
	err := wrapNodeError(nodeErr, cause)

	var gotNodeErr *NodeError
	if !errors.As(err, &gotNodeErr) || gotNodeErr != nodeErr {
		t.Fatalf("wrapped error = %v, want NodeError %p", err, nodeErr)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("wrapped error = %v, want cause %v", err, cause)
	}
}
