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
	"time"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

// FetchContainer fetches public container metadata from a Node Agent.
func (c *NodeClient) FetchContainer(
	ctx context.Context,
	address NodeAddress,
	containerID string,
) (metadata *nodeapi.ContainerMetadata, returnedErr error) {
	serverURL, err := address.serverURL()
	if err != nil {
		return nil, err
	}

	startedAt := time.Now()
	defer func() {
		if c.observe != nil {
			c.observe("container.fetch", time.Since(startedAt), returnedErr)
		}
	}()

	generated, err := c.generatedClient(serverURL)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	response, err := generated.GetContainer(requestCtx, containerID)
	if err != nil {
		return nil, wrapNodeError(&NodeError{
			Code:    NodeErrorCodeTransport,
			Message: "fetch container metadata",
		}, err)
	}
	return parseNodeContainerResponse(response, containerID)
}
