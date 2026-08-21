// Copyright 2025, 2026 The HuaTuo Authors
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
	"encoding/json"
	"errors"
	"net/http"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
)

type ConfigHandler struct {
	Handlers []server.Route
}

type ConfigRequest struct {
	Config map[string]json.RawMessage `json:"config"`
}

func NewConfigHandler() *ConfigHandler {
	h := &ConfigHandler{}
	h.Handlers = []server.Route{
		{Method: http.MethodPut, Path: "/config", Handler: h.update},
	}
	return h
}

func (h *ConfigHandler) update(ctx *server.Context) error {
	req := ConfigRequest{}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	values := make(map[string]any, len(req.Config))
	for key, value := range req.Config {
		values[key] = value
	}
	if err := config.UpdateAndSync(values); err != nil {
		if errors.Is(err, config.ErrInvalidUpdate) {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
		log.Errorf("failed to persist config: %v", err)
		return response.ErrInternal.WithMessage("failed to persist config")
	}

	ctx.Status(http.StatusNoContent)
	return nil
}
