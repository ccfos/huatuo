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
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestReadResponseBodyClassifiesReadFailureAsTransportError(t *testing.T) {
	readErr := errors.New("read response body")
	response := jsonResponse(http.StatusOK, "")
	response.Body = io.NopCloser(io.MultiReader(
		strings.NewReader("{"),
		iotest.ErrReader(readErr),
	))
	defer response.Body.Close()

	_, err := readResponseBody(response, maxSuccessBodyBytes)
	var nodeErr *Error
	if !errors.As(err, &nodeErr) {
		t.Fatalf("readResponseBody() error = %v, want *Error", err)
	}
	if nodeErr.Code != ErrorCodeClientTransport || nodeErr.StatusCode != http.StatusOK {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("readResponseBody() error = %v, want read error", err)
	}
}

func TestReadResponseBodyClassifiesOversizedBodyAsProtocolError(t *testing.T) {
	response := jsonResponse(http.StatusOK, strings.Repeat("x", maxSuccessBodyBytes+1))
	defer response.Body.Close()

	_, err := readResponseBody(response, maxSuccessBodyBytes)
	var nodeErr *Error
	if !errors.As(err, &nodeErr) {
		t.Fatalf("readResponseBody() error = %v, want *Error", err)
	}
	if nodeErr.Code != ErrorCodeClientProtocol || nodeErr.StatusCode != http.StatusOK {
		t.Fatalf("Node client error = %+v", nodeErr)
	}
}

func TestParseErrorRejectsProtocolViolations(t *testing.T) {
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
			err := parseError(tt.statusCode, []byte(tt.body))
			var nodeErr *Error
			if !errors.As(err, &nodeErr) {
				t.Fatalf("parseError() error = %v, want *Error", err)
			}
			if nodeErr.Code != ErrorCodeClientProtocol || nodeErr.StatusCode != tt.statusCode {
				t.Fatalf("Node client error = %+v", nodeErr)
			}
		})
	}
}
