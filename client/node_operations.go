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
	"time"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

// sendNodeOperationRequest adapts generated Operation methods with different
// arguments to the shared request execution lifecycle.
type sendNodeOperationRequest func(context.Context, *nodeapi.Client) (*http.Response, error)

// StartOperation sends one unified Node Operation start request.
func (c *NodeClient) StartOperation(
	ctx context.Context,
	address NodeAddress,
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
		address,
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
	address NodeAddress,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.executeOperation(
		ctx,
		address,
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
	address NodeAddress,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.executeOperation(
		ctx,
		address,
		"operation.stop",
		requestID,
		nodeSuccessResponseOKOrAccepted,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.StopOperation(ctx, requestID)
		},
	)
}

func (c *NodeClient) executeOperation(
	ctx context.Context,
	address NodeAddress,
	operationName string,
	requestID string,
	successMode nodeSuccessResponseMode,
	send sendNodeOperationRequest,
) (result *nodeapi.Operation, returnedErr error) {
	serverURL, err := address.serverURL()
	if err != nil {
		return nil, err
	}

	// Start and Stop are not safely retryable when the response is lost.
	startedAt := time.Now()
	defer func() {
		if c.observe != nil {
			c.observe(operationName, time.Since(startedAt), returnedErr)
		}
	}()

	generated, err := c.generatedClient(serverURL)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	response, err := send(requestCtx, generated)
	if err != nil {
		return nil, wrapNodeError(&NodeError{
			Code:    NodeErrorCodeTransport,
			Message: operationName + " Node API request",
		}, err)
	}
	return parseNodeOperationResponse(response, requestID, successMode)
}
