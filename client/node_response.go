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
	"fmt"
	"io"
	"net/http"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
)

const (
	maxNodeSuccessBodyBytes = 1 << 20
	maxNodeErrorBodyBytes   = 8 << 10
)

func readNodeResponseBody(response *http.Response, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, wrapNodeError(&NodeError{
			StatusCode: response.StatusCode,
			Code:       NodeErrorCodeTransport,
			Message:    "read Node API response",
		}, err)
	}
	if int64(len(body)) > limit {
		return nil, newNodeProtocolError(
			response.StatusCode,
			fmt.Sprintf("Node API response exceeds %d bytes", limit),
		)
	}
	return body, nil
}

func parseNodeError(statusCode int, body []byte) error {
	var envelope apiv1.ErrorResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return wrapNodeProtocolError(statusCode, "decode Node API error response", err)
	}
	code := envelope.Error.Code
	expectedStatus, ok := nodeapi.HTTPStatusForErrorCode(code)
	if !ok {
		return newNodeProtocolError(statusCode, fmt.Sprintf("unknown Node error code %q", code))
	}
	if expectedStatus != statusCode {
		return newNodeProtocolError(
			statusCode,
			fmt.Sprintf(
				"Node error code %q requires HTTP %d",
				code,
				expectedStatus,
			),
		)
	}
	if envelope.Error.Message == "" {
		return newNodeProtocolError(
			statusCode,
			fmt.Sprintf("Node error code %q has an empty message", code),
		)
	}
	return &NodeError{
		StatusCode: statusCode,
		Code:       code,
		Message:    envelope.Error.Message,
	}
}

func newNodeProtocolError(statusCode int, message string) *NodeError {
	return &NodeError{
		StatusCode: statusCode,
		Code:       NodeErrorCodeProtocol,
		Message:    message,
	}
}

func wrapNodeProtocolError(statusCode int, message string, cause error) error {
	return wrapNodeError(newNodeProtocolError(statusCode, message), cause)
}
