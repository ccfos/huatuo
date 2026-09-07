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
	"fmt"

	apiv1 "huatuo-bamai/apis/v1"
)

const (
	// ErrorCodeClientInvalidArgument identifies a request rejected before dispatch.
	ErrorCodeClientInvalidArgument apiv1.ErrorCode = "client_invalid_argument"
	// ErrorCodeClientProtocol identifies a response that violates the Node API contract.
	ErrorCodeClientProtocol apiv1.ErrorCode = "client_protocol"
	// ErrorCodeClientTransport identifies a failure to complete HTTP transport handling.
	ErrorCodeClientTransport apiv1.ErrorCode = "client_transport"
)

// Error describes either a Node API error response or a client-side failure.
type Error struct {
	StatusCode int
	Code       apiv1.ErrorCode
	Message    string
}

// Error formats the stable error without exposing response bodies.
func (e *Error) Error() string {
	if e == nil {
		return "node client error"
	}
	if isClientErrorCode(e.Code) {
		if e.StatusCode == 0 {
			return fmt.Sprintf("node client %s: %s", e.Code, e.Message)
		}
		return fmt.Sprintf(
			"node client %s for HTTP %d: %s",
			e.Code,
			e.StatusCode,
			e.Message,
		)
	}
	return fmt.Sprintf("node API returned HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
}

func isClientErrorCode(code apiv1.ErrorCode) bool {
	switch code {
	case ErrorCodeClientInvalidArgument,
		ErrorCodeClientProtocol,
		ErrorCodeClientTransport:
		return true
	default:
		return false
	}
}

func wrapError(nodeErr *Error, cause error) error {
	return fmt.Errorf("%w: %w", nodeErr, cause)
}
