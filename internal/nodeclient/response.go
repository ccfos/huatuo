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

type successResponseMode uint8

const (
	successResponseOK successResponseMode = iota
	successResponseOKOrAccepted
)

func parseResponse(
	response *http.Response,
	requestID string,
	successMode successResponseMode,
) (*nodeapi.Operation, error) {
	if response == nil {
		return nil, &Error{
			Code:    ErrorCodeClientProtocol,
			Message: "Node API returned a nil response",
		}
	}
	if response.Body == nil {
		return nil, &Error{
			StatusCode: response.StatusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "Node API returned a nil response body",
		}
	}

	limit := int64(maxErrorBodyBytes)
	if response.StatusCode == http.StatusOK ||
		successMode == successResponseOKOrAccepted && response.StatusCode == http.StatusAccepted {
		limit = maxSuccessBodyBytes
	}
	body, readErr := readBody(response.Body, limit)
	closeErr := response.Body.Close()
	if readErr != nil {
		return nil, wrapError(&Error{
			StatusCode: response.StatusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "read Node API response",
		}, readErr)
	}
	if closeErr != nil {
		return nil, wrapError(&Error{
			StatusCode: response.StatusCode,
			Code:       ErrorCodeClientTransport,
			Message:    "close Node API response",
		}, closeErr)
	}
	switch response.StatusCode {
	case http.StatusOK:
		return parseOperation(response.StatusCode, body, requestID)
	case http.StatusAccepted:
		if successMode == successResponseOKOrAccepted {
			return parseOperation(response.StatusCode, body, requestID)
		}
		return nil, &Error{
			StatusCode: response.StatusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "unexpected HTTP 202 success response",
		}
	default:
		return nil, parseError(response.StatusCode, body)
	}
}

func readBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func parseOperation(statusCode int, body []byte, requestID string) (*nodeapi.Operation, error) {
	var envelope nodeapi.OperationResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, wrapError(&Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "decode operation response",
		}, err)
	}
	operation := envelope.Data
	if operation.RequestID != requestID {
		return nil, &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message: fmt.Sprintf(
				"response request ID %q does not match %q",
				operation.RequestID,
				requestID,
			),
		}
	}
	if !operation.Status.Valid() {
		return nil, &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    fmt.Sprintf("unsupported operation status %q", operation.Status),
		}
	}
	if operation.Status == nodeapi.OperationStatusTerminal {
		if operation.Terminal == nil || !operation.Terminal.Outcome.Valid() {
			return nil, &Error{
				StatusCode: statusCode,
				Code:       ErrorCodeClientProtocol,
				Message:    "terminal operation has an invalid outcome",
			}
		}
		if operation.Terminal.Outcome == nodeapi.OperationOutcomeFailed &&
			(operation.Terminal.Reason == nil || *operation.Terminal.Reason == "") {
			return nil, &Error{
				StatusCode: statusCode,
				Code:       ErrorCodeClientProtocol,
				Message:    "failed operation has no reason",
			}
		}
	} else if operation.Terminal != nil {
		return nil, &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    fmt.Sprintf("operation status %q contains terminal details", operation.Status),
		}
	}
	return &operation, nil
}

func parseError(statusCode int, body []byte) error {
	var envelope apiv1.ErrorResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return wrapError(&Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    "decode Node API error response",
		}, err)
	}
	code := envelope.Error.Code
	expectedStatus, ok := nodeapi.HTTPStatusForErrorCode(code)
	if !ok {
		return &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    fmt.Sprintf("unknown Node error code %q", code),
		}
	}
	if expectedStatus != statusCode {
		return &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message: fmt.Sprintf(
				"Node error code %q requires HTTP %d",
				code,
				expectedStatus,
			),
		}
	}
	if envelope.Error.Message == "" {
		return &Error{
			StatusCode: statusCode,
			Code:       ErrorCodeClientProtocol,
			Message:    fmt.Sprintf("Node error code %q has an empty message", code),
		}
	}
	return &Error{
		StatusCode: statusCode,
		Code:       code,
		Message:    envelope.Error.Message,
	}
}
