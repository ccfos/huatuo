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
	"context"
	"net/http"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

// StartOperation sends one unified Node Operation start request.
func (c *NodeClient) StartOperation(
	ctx context.Context,
	host string,
	request *nodeapi.StartOperationRequest,
) (*nodeapi.Operation, error) {
	if request == nil {
		return nil, &NodeError{
			Code:    NodeErrorCodeInvalidArgument,
			Message: "operation request is required",
		}
	}
	return c.executeOperation(
		ctx,
		host,
		"operation.start",
		request.RequestID,
		nodeSuccessResponseOKOrAccepted,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.StartOperation(ctx, *request)
		},
	)
}

// GetOperation sends one unified Node Operation get request.
func (c *NodeClient) GetOperation(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.executeOperation(
		ctx,
		host,
		"operation.get",
		requestID,
		nodeSuccessResponseOK,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.GetOperation(ctx, requestID)
		},
	)
}

// StopOperation sends one unified Node Operation stop request.
func (c *NodeClient) StopOperation(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.executeOperation(
		ctx,
		host,
		"operation.stop",
		requestID,
		nodeSuccessResponseOKOrAccepted,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.StopOperation(ctx, requestID)
		},
	)
}
