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

package handlers

import (
	"net/http"

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
)

type ContainerHandler struct {
	Handlers []server.Handle
}

type ContainersJSONReq struct {
	ContainerID string `form:"container_id" binding:"omitempty,alphanum,len=64"`
}

func NewContainerHandler() *ContainerHandler {
	h := &ContainerHandler{}
	h.Handlers = []server.Handle{
		{Typ: server.HttpGet, Uri: "/containers/json", Handle: h.list},
	}
	return h
}

func (h *ContainerHandler) list(ctx *server.Context) error {
	req := &ContainersJSONReq{}
	if err := ctx.ShouldBindQuery(req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	all, err := pod.Containers()
	if err != nil {
		return response.NewAPIError(500, err.Error(), http.StatusInternalServerError)
	}

	resp := make([]*pod.Container, 0, len(all))
	for _, container := range all {
		if req.ContainerID != "" && req.ContainerID != container.ID {
			continue
		}
		resp = append(resp, container)
	}

	response.Success(ctx, resp)
	return nil
}
