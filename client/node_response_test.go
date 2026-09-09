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
	"testing/iotest"
)

func nodeJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestReadResponseBodyClassifiesReadFailureAsTransportError(t *testing.T) {
	readErr := errors.New("read response body")
	response := nodeJSONResponse(http.StatusOK, "")
	response.Body = io.NopCloser(io.MultiReader(
		strings.NewReader("{"),
		iotest.ErrReader(readErr),
	))
	defer response.Body.Close()

	_, err := readNodeResponseBody(response, maxNodeSuccessBodyBytes)
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("readNodeResponseBody() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != NodeErrorCodeTransport || nodeErr.StatusCode != http.StatusOK {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("readNodeResponseBody() error = %v, want read error", err)
	}
}

func TestReadResponseBodyClassifiesOversizedBodyAsProtocolError(t *testing.T) {
	response := nodeJSONResponse(http.StatusOK, strings.Repeat("x", maxNodeSuccessBodyBytes+1))
	defer response.Body.Close()

	_, err := readNodeResponseBody(response, maxNodeSuccessBodyBytes)
	var nodeErr *NodeError
	if !errors.As(err, &nodeErr) {
		t.Fatalf("readNodeResponseBody() error = %v, want *NodeError", err)
	}
	if nodeErr.Code != NodeErrorCodeProtocol || nodeErr.StatusCode != http.StatusOK {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
}

func TestParseNodeErrorRejectsProtocolViolations(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
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
			err := parseNodeError(tt.statusCode, []byte(tt.body))
			var nodeErr *NodeError
			if !errors.As(err, &nodeErr) {
				t.Fatalf("parseNodeError() error = %v, want *NodeError", err)
			}
			if nodeErr.Code != NodeErrorCodeProtocol || nodeErr.StatusCode != tt.statusCode {
				t.Fatalf("Node client error = %+v", nodeErr)
			}
		})
	}
}
