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
	"encoding/json"
	"fmt"
	"time"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

// UpdateConfig updates Node Agent configuration with one HTTP request. It does
// not retry because a lost response cannot show whether the update was applied.
func (c *NodeClient) UpdateConfig(
	ctx context.Context,
	address NodeAddress,
	request *nodeapi.UpdateConfigRequest,
) (returnedErr error) {
	if request == nil {
		return &NodeError{
			Code:    NodeErrorCodeInvalidArgument,
			Message: "config update request is required",
		}
	}
	if len(request.Config) == 0 {
		return &NodeError{
			Code:    NodeErrorCodeInvalidArgument,
			Message: "config update request must contain at least one value",
		}
	}
	for key, value := range request.Config {
		if !json.Valid(value) {
			return &NodeError{
				Code:    NodeErrorCodeInvalidArgument,
				Message: fmt.Sprintf("config update value %q must be valid JSON", key),
			}
		}
	}
	serverURL, err := address.serverURL()
	if err != nil {
		return err
	}

	startedAt := time.Now()
	defer func() {
		if c.observe != nil {
			c.observe("config.update", time.Since(startedAt), returnedErr)
		}
	}()

	generated, err := c.generatedClient(serverURL)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()

	response, err := generated.UpdateConfig(requestCtx, *request)
	if err != nil {
		return wrapNodeError(&NodeError{
			Code:    NodeErrorCodeTransport,
			Message: "update Node config",
		}, err)
	}
	return parseNodeConfigResponse(response)
}
