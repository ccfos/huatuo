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
	"fmt"
	"io"
	"net/http"
	"testing"

	nodeapi "huatuo-bamai/apis/v1/node"
)

type nodeCloseErrorBody struct {
	io.Reader
	err error
}

func (b nodeCloseErrorBody) Close() error { return b.err }

func nodeOperationJSON(requestID, status string) string {
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

func TestParseNodeOperationResponseRejectsProtocolViolations(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		successMode nodeSuccessResponseMode
	}{
		{
			name:        "accepted get response",
			statusCode:  http.StatusAccepted,
			body:        nodeOperationJSON("job-1", "pending"),
			successMode: nodeSuccessResponseOK,
		},
		{
			name:       "mismatched response ID",
			statusCode: http.StatusOK,
			body:       nodeOperationJSON("other-job", "running"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := nodeJSONResponse(tt.statusCode, tt.body)
			defer response.Body.Close()

			_, err := parseNodeOperationResponse(response, "job-1", tt.successMode)
			var nodeErr *NodeError
			if !errors.As(err, &nodeErr) {
				t.Fatalf("parseNodeOperationResponse() error = %v, want *NodeError", err)
			}
			if nodeErr.Code != NodeErrorCodeProtocol || nodeErr.StatusCode != tt.statusCode {
				t.Fatalf("Node client error = %+v", nodeErr)
			}
		})
	}
}

func TestParseNodeOperationResponseUsesBodyInsteadOfContentType(t *testing.T) {
	for _, test := range []struct {
		name        string
		contentType string
	}{
		{name: "missing", contentType: ""},
		{name: "inaccurate", contentType: "text/plain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := nodeJSONResponse(http.StatusOK, nodeOperationJSON("job-1", "pending"))
			response.Header.Set("Content-Type", test.contentType)

			operation, err := parseNodeOperationResponse(response, "job-1", nodeSuccessResponseOK)
			if err != nil {
				t.Fatalf("parseNodeOperationResponse() error = %v", err)
			}
			if operation.RequestID != "job-1" {
				t.Fatalf(
					"parseNodeOperationResponse() request ID = %q, want %q",
					operation.RequestID,
					"job-1",
				)
			}
		})
	}
}

func TestParseNodeOperationResponseParsesTerminalOperation(t *testing.T) {
	response := nodeJSONResponse(http.StatusOK, nodeOperationJSON("job-1", "completed"))
	response.Body = nodeCloseErrorBody{
		Reader: response.Body,
		err:    errors.New("close response body"),
	}

	operation, err := parseNodeOperationResponse(response, "job-1", nodeSuccessResponseOK)
	if err != nil {
		t.Fatalf("parseNodeOperationResponse() error = %v", err)
	}
	if operation.Status != nodeapi.OperationStatusTerminal {
		t.Fatalf(
			"parseNodeOperationResponse() status = %q, want %q",
			operation.Status,
			nodeapi.OperationStatusTerminal,
		)
	}
}
