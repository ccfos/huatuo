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
	"errors"
	"net/http"

	"huatuo-bamai/internal/log"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"

	"github.com/gin-gonic/gin/binding"
)

func handleProto[Request, Response any](
	ctx *server.Context,
	operation string,
	invoke func(context.Context, *Request) (*Response, error),
) error {
	req := new(Request)
	if err := ctx.ShouldBindBodyWith(req, binding.ProtoBuf); err != nil {
		return response.ErrInvalidRequest.WithMessage("invalid protobuf request")
	}

	resp, err := invoke(ctx.Request().Context(), req)
	if err != nil {
		if errors.Is(err, profileService.ErrInvalidQuery) {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
		if errors.Is(err, profileService.ErrProfilesAbsent) {
			return response.ErrNotFound.WithMessage("profiles not found")
		}
		log.WithError(err).WithField("operation", operation).Error("profile query failed")
		return response.ErrInternal
	}

	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, resp)
	return nil
}

func (h *Handler) displaySelectMergeStacktraces(ctx *server.Context) error {
	return handleProto(ctx, "select_merge_stacktraces", h.profileQueryService.SelectMergeStacktraces)
}

func (h *Handler) displayProfileTypes(ctx *server.Context) error {
	return handleProto(ctx, "profile_types", h.profileQueryService.ProfileTypes)
}

func (h *Handler) displayLabelNames(ctx *server.Context) error {
	return handleProto(ctx, "label_names", h.profileQueryService.LabelNames)
}

func (h *Handler) displayLabelValues(ctx *server.Context) error {
	return handleProto(ctx, "label_values", h.profileQueryService.LabelValues)
}
