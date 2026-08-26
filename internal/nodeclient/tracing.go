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

// StartTracing sends one tracing Start request.
func (c *Client) StartTracing(
	ctx context.Context,
	host string,
	request *nodeapi.StartTracingRequest,
) (*nodeapi.Operation, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: tracing request is required", ErrInvalidArgument)
	}
	return c.execute(
		ctx,
		host,
		"tracing.start",
		request.RequestID,
		successResponseOKOrAccepted,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.StartTracing(ctx, *request)
		},
	)
}

// GetTracing sends one tracing Get request.
func (c *Client) GetTracing(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.execute(
		ctx,
		host,
		"tracing.get",
		requestID,
		successResponseOK,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.GetTracing(ctx, requestID)
		},
	)
}

// StopTracing sends one tracing Stop request.
func (c *Client) StopTracing(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.execute(
		ctx,
		host,
		"tracing.stop",
		requestID,
		successResponseOKOrAccepted,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.StopTracing(ctx, requestID)
		},
	)
}
