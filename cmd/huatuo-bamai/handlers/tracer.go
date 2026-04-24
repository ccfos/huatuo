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

	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/tracing"
)

type TracerHandler struct {
	tracingManager *tracing.TracingManager
	Handlers       []server.Handle
}

func NewTracerHandler(mgrTracing *tracing.TracingManager) *TracerHandler {
	h := &TracerHandler{tracingManager: mgrTracing}
	h.Handlers = []server.Handle{
		{Typ: server.HttpGet, Uri: "", Handle: h.list},
		{Typ: server.HttpPut, Uri: "/:name/start", Handle: h.start},
		{Typ: server.HttpPut, Uri: "/:name/stop", Handle: h.stop},
	}
	return h
}

func (h *TracerHandler) list(ctx *server.Context) error {
	response.Success(ctx, h.tracingManager.Dump())
	return nil
}

func (h *TracerHandler) start(ctx *server.Context) error {
	name := ctx.Param("name")
	if name == "" {
		return response.ErrInvalidRequest.WithMessage("missing tracer name")
	}

	if err := h.tracingManager.StartByName(name); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	ctx.Status(http.StatusNoContent)
	return nil
}

func (h *TracerHandler) stop(ctx *server.Context) error {
	name := ctx.Param("name")
	if name == "" {
		return response.ErrInvalidRequest.WithMessage("missing tracer name")
	}

	if err := h.tracingManager.StopByName(name); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	ctx.Status(http.StatusNoContent)
	return nil
}
