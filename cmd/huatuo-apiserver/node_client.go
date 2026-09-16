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

package main

import (
	"context"
	"net"
	"strconv"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	"github.com/ccfos/huatuo/client"
	"github.com/ccfos/huatuo/internal/job"
)

// nodeOperationClient keeps the Job hostname independent of Node Agent routing.
type nodeOperationClient struct {
	nodeClient *client.NodeClient
	port       string
}

func newNodeOperationClient(
	nodeClient *client.NodeClient,
	port int,
) *nodeOperationClient {
	return &nodeOperationClient{
		nodeClient: nodeClient,
		port:       strconv.Itoa(port),
	}
}

func (c *nodeOperationClient) StartOperation(
	ctx context.Context,
	host string,
	request *nodeapi.StartOperationRequest,
) (*nodeapi.Operation, error) {
	return c.nodeClient.StartOperation(ctx, c.address(host), request)
}

func (c *nodeOperationClient) GetOperation(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.nodeClient.GetOperation(ctx, c.address(host), requestID)
}

func (c *nodeOperationClient) StopOperation(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	return c.nodeClient.StopOperation(ctx, c.address(host), requestID)
}

func (c *nodeOperationClient) address(host string) client.NodeAddress {
	return client.NodeAddress{HostPort: net.JoinHostPort(host, c.port)}
}

var _ job.NodeClient = (*nodeOperationClient)(nil)
