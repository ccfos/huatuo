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
	"context"
	"errors"
	"net/http"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/tracing"
)

type TracerHandler struct {
	tracingManager *tracing.Manager
	Handlers       []server.Handle
}

func NewTracerHandler(manager *tracing.Manager) *TracerHandler {
	h := &TracerHandler{tracingManager: manager}
	h.Handlers = []server.Handle{
		{Typ: server.HttpGet, Uri: "", Handle: h.list},
		{Typ: server.HttpPut, Uri: "/:name/start", Handle: h.start},
		{Typ: server.HttpPut, Uri: "/:name/stop", Handle: h.stop},
	}
	return h
}

func (h *TracerHandler) list(ctx *server.Context) error {
	response.Success(ctx, h.tracingManager.Snapshots())
	return nil
}

func (h *TracerHandler) start(ctx *server.Context) error {
	name := ctx.Param("name")
	if name == "" {
		return response.ErrInvalidRequest.WithMessage("missing tracer name")
	}

	tracerCtx := context.WithoutCancel(ctx.Request().Context())
	if err := h.tracingManager.StartByName(tracerCtx, name); err != nil {
		return tracerAPIError(err)
	}

	ctx.Status(http.StatusNoContent)
	return nil
}

func (h *TracerHandler) stop(ctx *server.Context) error {
	name := ctx.Param("name")
	if name == "" {
		return response.ErrInvalidRequest.WithMessage("missing tracer name")
	}

	if err := h.tracingManager.StopByName(ctx.Request().Context(), name); err != nil {
		return tracerAPIError(err)
	}

	ctx.Status(http.StatusNoContent)
	return nil
}

func tracerAPIError(err error) error {
	switch {
	case errors.Is(err, tracing.ErrTracerNotFound):
		return response.ErrNotFound.WithMessage(err.Error())
	case errors.Is(err, tracing.ErrTracerAlreadyRunning),
		errors.Is(err, tracing.ErrTracerNotRunning),
		errors.Is(err, tracing.ErrManagerClosed):
		return response.ErrConflict.WithMessage(err.Error())
	case errors.Is(err, context.Canceled):
		return response.ErrInternal.WithMessage("tracer operation canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return response.ErrInternal.WithMessage("tracer operation timed out")
	default:
		log.WithError(err).Error("tracer operation failed")
		return response.ErrInternal.WithMessage("tracer operation failed")
	}
}
