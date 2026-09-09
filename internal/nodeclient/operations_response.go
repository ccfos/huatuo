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
	"net/http"

	nodeapi "huatuo-bamai/apis/v1/node"
)

type successResponseMode uint8

const (
	successResponseOK successResponseMode = iota
	successResponseOKOrAccepted
)

func parseOperationResponse(
	response *http.Response,
	requestID string,
	successMode successResponseMode,
) (*nodeapi.Operation, error) {
	defer response.Body.Close()

	limit := int64(maxErrorBodyBytes)
	if response.StatusCode == http.StatusOK ||
		successMode == successResponseOKOrAccepted && response.StatusCode == http.StatusAccepted {
		limit = maxSuccessBodyBytes
	}
	body, err := readResponseBody(response, limit)
	if err != nil {
		return nil, err
	}

	switch response.StatusCode {
	case http.StatusOK:
		return parseOperation(response.StatusCode, body, requestID)
	case http.StatusAccepted:
		if successMode == successResponseOKOrAccepted {
			return parseOperation(response.StatusCode, body, requestID)
		}
		return nil, newProtocolError(response.StatusCode, "unexpected HTTP 202 success response")
	default:
		return nil, parseError(response.StatusCode, body)
	}
}

func parseOperation(statusCode int, body []byte, requestID string) (*nodeapi.Operation, error) {
	var envelope nodeapi.OperationResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, wrapProtocolError(statusCode, "decode operation response", err)
	}
	operation := envelope.Data
	if err := validateOperation(statusCode, &operation, requestID); err != nil {
		return nil, err
	}
	return &operation, nil
}

func validateOperation(statusCode int, operation *nodeapi.Operation, requestID string) error {
	if operation.RequestID != requestID {
		return newProtocolError(
			statusCode,
			fmt.Sprintf(
				"response request ID %q does not match %q",
				operation.RequestID,
				requestID,
			),
		)
	}
	if !operation.Status.Valid() {
		return newProtocolError(
			statusCode,
			fmt.Sprintf("unsupported operation status %q", operation.Status),
		)
	}
	if operation.Status == nodeapi.OperationStatusTerminal {
		if operation.Terminal == nil || !operation.Terminal.Outcome.Valid() {
			return newProtocolError(statusCode, "terminal operation has an invalid outcome")
		}
		if operation.Terminal.Outcome == nodeapi.OperationOutcomeFailed &&
			(operation.Terminal.Reason == nil || *operation.Terminal.Reason == "") {
			return newProtocolError(statusCode, "failed operation has no reason")
		}
	} else if operation.Terminal != nil {
		return newProtocolError(
			statusCode,
			fmt.Sprintf("operation status %q contains terminal details", operation.Status),
		)
	}
	return nil
}
