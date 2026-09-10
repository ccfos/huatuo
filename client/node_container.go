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
	"net/url"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
)

// FetchNodeContainer fetches public container metadata from huatuo-bamai.
func FetchNodeContainer(
	ctx context.Context,
	serverAddr string,
	containerID string,
) (*nodeapi.ContainerMetadata, error) {
	generated, err := nodeapi.NewClient(
		(&url.URL{
			Scheme: "http",
			Host:   serverAddr,
		}).String(),
		nodeapi.WithHTTPClient(&http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}),
	)
	if err != nil {
		return nil, wrapNodeError(&NodeError{
			Code:    NodeErrorCodeInvalidArgument,
			Message: "create container metadata client",
		}, err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, defaultNodeRequestTimeout)
	defer cancel()
	response, err := generated.GetContainer(requestCtx, containerID)
	if err != nil {
		return nil, wrapNodeError(&NodeError{
			Code:    NodeErrorCodeTransport,
			Message: "get container metadata",
		}, err)
	}
	return parseNodeContainerResponse(response, containerID)
}
