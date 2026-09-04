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
	"context"
	"fmt"
	"net/http"

	nodeapi "huatuo-bamai/apis/v1/node"
)

// StartOperation sends one unified Node Operation start request.
func (c *Client) StartOperation(
	ctx context.Context,
	host string,
	request *nodeapi.StartOperationRequest,
) (*nodeapi.Operation, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: operation request is required", ErrInvalidArgument)
	}
	return c.execute(
		ctx,
		host,
		"operation.start",
		request.RequestID,
		successResponseOKOrAccepted,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.StartOperation(ctx, *request)
		},
	)
}

// GetOperation sends one unified Node Operation get request.
func (c *Client) GetOperation(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.execute(
		ctx,
		host,
		"operation.get",
		requestID,
		successResponseOK,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.GetOperation(ctx, requestID)
		},
	)
}

// StopOperation sends one unified Node Operation stop request.
func (c *Client) StopOperation(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.execute(
		ctx,
		host,
		"operation.stop",
		requestID,
		successResponseOKOrAccepted,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.StopOperation(ctx, requestID)
		},
	)
}
