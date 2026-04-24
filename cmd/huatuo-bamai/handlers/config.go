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
	"reflect"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
)

type ConfigHandler struct {
	Handlers []server.Handle
}

type ConfigRequest struct {
	Config map[string]any `json:"config"`
}

func NewConfigHandler() *ConfigHandler {
	h := &ConfigHandler{}
	h.Handlers = []server.Handle{
		{Typ: server.HttpPut, Uri: "/config", Handle: h.update},
	}
	return h
}

func (h *ConfigHandler) update(ctx *server.Context) error {
	req := ConfigRequest{}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	for k, v := range req.Config {
		if reflect.ValueOf(v).Kind() == reflect.Float64 {
			v = int(v.(float64))
		}
		config.Set(k, v)
	}

	if err := config.Sync(); err != nil {
		log.Warnf("config sync error: %v", err)
		return response.ErrInternal.WithMessage(err.Error())
	}

	ctx.Status(http.StatusNoContent)
	return nil
}
