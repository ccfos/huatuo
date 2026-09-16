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

package handlers

import (
	"context"
	"fmt"

	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	"github.com/ccfos/huatuo/internal/server/response"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"
)

// GetContainer returns the metadata needed to target a container.
func (h *NodeAPIHandler) GetContainer(
	_ context.Context,
	request nodeapi.GetContainerRequestObject,
) (nodeapi.GetContainerResponseObject, error) {
	container, err := h.containerByID(request.ContainerID)
	if err != nil {
		return nil, fmt.Errorf("get container %q: %w", request.ContainerID, err)
	}
	if container == nil {
		return nil, response.NewAPIError(
			nodeapi.ErrorCodeContainerNotFound,
			"container was not found",
		)
	}

	cgroupCSS := make(map[string]string, len(container.CgroupCss))
	for subsystem, address := range container.CgroupCss {
		if encoded := kernaddr.Format(address); encoded != "" {
			cgroupCSS[subsystem] = encoded
		}
	}
	return nodeapi.GetContainer200JSONResponse{
		Data: nodeapi.ContainerMetadata{
			ID:        container.ID,
			CgroupCSS: cgroupCSS,
		},
	}, nil
}
