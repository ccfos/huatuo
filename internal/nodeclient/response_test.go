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
)

type closeErrorBody struct {
	io.Reader
	err error
}

func (b closeErrorBody) Close() error { return b.err }

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
