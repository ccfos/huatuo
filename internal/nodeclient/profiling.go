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

// StartProfiling sends one profiling Start request.
func (c *Client) StartProfiling(
	ctx context.Context,
	host string,
	request *nodeapi.StartProfilingRequest,
) (*nodeapi.Operation, error) {
	if request == nil {
		return nil, fmt.Errorf("%w: profiling request is required", ErrInvalidArgument)
	}
	return c.execute(
		ctx,
		host,
		"profiling.start",
		request.RequestID,
		successResponseOKOrAccepted,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.StartProfiling(ctx, *request)
		},
	)
}

// GetProfiling sends one profiling Get request.
func (c *Client) GetProfiling(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.execute(
		ctx,
		host,
		"profiling.get",
		requestID,
		successResponseOK,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.GetProfiling(ctx, requestID)
		},
	)
}

// StopProfiling sends one profiling Stop request.
func (c *Client) StopProfiling(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.execute(
		ctx,
		host,
		"profiling.stop",
		requestID,
		successResponseOKOrAccepted,
		func(ctx context.Context, generated *nodeapi.Client) (*http.Response, error) {
			return generated.StopProfiling(ctx, requestID)
		},
	)
}
