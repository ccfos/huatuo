// Copyright 2025 The HuaTuo Authors
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

package services

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"huatuo-bamai/internal/pod"
)

// ContainersJsonReq represents the containers json request.
type ContainersJsonReq struct {
	ContainerID string `form:"container_id" binding:"omitempty,alphanum,len=64"`
}

// ContainersList handles the containers list request.
func ContainersList(ctx *gin.Context) {
	req := &ContainersJsonReq{}
	if err := ctx.ShouldBindQuery(req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	all, err := pod.Containers()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp := make([]*pod.Container, 0, len(all))
	for _, container := range all {
		if req.ContainerID != "" && req.ContainerID != container.ID {
			continue
		}

		resp = append(resp, container)
	}

	ctx.JSON(http.StatusOK, resp)
}
