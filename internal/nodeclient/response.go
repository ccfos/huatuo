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
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
)

const (
	maxSuccessBodyBytes = 1 << 20
	maxErrorBodyBytes   = 8 << 10
)

func readResponseBody(response *http.Response, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, wrapError(&Error{
			StatusCode: response.StatusCode,
			Code:       ErrorCodeClientTransport,
			Message:    "read Node API response",
		}, err)
	}
	if int64(len(body)) > limit {
		return nil, newProtocolError(
			response.StatusCode,
			fmt.Sprintf("Node API response exceeds %d bytes", limit),
		)
	}
	return body, nil
}

func parseError(statusCode int, body []byte) error {
	var envelope apiv1.ErrorResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return wrapProtocolError(statusCode, "decode Node API error response", err)
	}
	code := envelope.Error.Code
	expectedStatus, ok := nodeapi.HTTPStatusForErrorCode(code)
	if !ok {
		return newProtocolError(statusCode, fmt.Sprintf("unknown Node error code %q", code))
	}
	if expectedStatus != statusCode {
		return newProtocolError(
			statusCode,
			fmt.Sprintf(
				"Node error code %q requires HTTP %d",
				code,
				expectedStatus,
			),
		)
	}
	if envelope.Error.Message == "" {
		return newProtocolError(
			statusCode,
			fmt.Sprintf("Node error code %q has an empty message", code),
		)
	}
	return &Error{
		StatusCode: statusCode,
		Code:       code,
		Message:    envelope.Error.Message,
	}
}

func newProtocolError(statusCode int, message string) *Error {
	return &Error{
		StatusCode: statusCode,
		Code:       ErrorCodeClientProtocol,
		Message:    message,
	}
}

func wrapProtocolError(statusCode int, message string, cause error) error {
	return wrapError(newProtocolError(statusCode, message), cause)
}
