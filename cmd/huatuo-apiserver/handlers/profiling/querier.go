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

package profiling

import (
	"context"
	"net/http"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/server"

	"github.com/gin-gonic/gin/binding"
)

func handleProto[Request, Response any](
	ctx *server.Context,
	operation string,
	invoke func(context.Context, *Request) (*Response, error),
) error {
	req := new(Request)
	if err := ctx.ShouldBindBodyWith(req, binding.ProtoBuf); err != nil {
		ctx.JSON(http.StatusBadRequest, map[string]any{"message": "invalid protobuf request"})
		return nil
	}

	resp, err := invoke(ctx.Request().Context(), req)
	if err != nil {
		log.WithError(err).WithField("operation", operation).Error("profile query failed")
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": "internal error"})
		return nil
	}

	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, resp)
	return nil
}

func (h *Handler) displaySelectMergeStacktraces(ctx *server.Context) error {
	return handleProto(ctx, "select_merge_stacktraces", h.profileService.SelectMergeStacktraces)
}

func (h *Handler) displayProfileTypes(ctx *server.Context) error {
	return handleProto(ctx, "profile_types", h.profileService.ProfileTypes)
}

func (h *Handler) displaySelectSeries(ctx *server.Context) error {
	return handleProto(ctx, "select_series", h.profileService.SelectSeries)
}

func (h *Handler) displayLabelNames(ctx *server.Context) error {
	return handleProto(ctx, "label_names", h.profileService.LabelNames)
}

func (h *Handler) displayLabelValues(ctx *server.Context) error {
	return handleProto(ctx, "label_values", h.profileService.LabelValues)
}
