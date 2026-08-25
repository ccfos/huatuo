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
	profileservice "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"

	"github.com/gin-gonic/gin/binding"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
)

// ProfileQueryService is the Pyroscope-compatible query surface.
type ProfileQueryService interface {
	SelectMergeStacktraces(
		ctx context.Context,
		req *querierv1.SelectMergeStacktracesRequest,
	) (*querierv1.SelectMergeStacktracesResponse, error)
	ProfileTypes(
		ctx context.Context,
		req *querierv1.ProfileTypesRequest,
	) (*querierv1.ProfileTypesResponse, error)
	LabelNames(
		ctx context.Context,
		req *typesv1.LabelNamesRequest,
	) (*typesv1.LabelNamesResponse, error)
	LabelValues(
		ctx context.Context,
		req *typesv1.LabelValuesRequest,
	) (*typesv1.LabelValuesResponse, error)
}

type queryHandler struct {
	service ProfileQueryService
}

// QueryRoutes returns the non-JSON Pyroscope compatibility routes.
func QueryRoutes(service ProfileQueryService) []server.Route {
	if service == nil {
		return nil
	}
	handler := &queryHandler{service: service}
	return []server.Route{
		{
			Method:  http.MethodPost,
			Path:    "/flamegraph/querier.v1.QuerierService/SelectMergeStacktraces",
			Handler: handler.selectMergeStacktraces,
		},
		{
			Method:  http.MethodPost,
			Path:    "/flamegraph/querier.v1.QuerierService/ProfileTypes",
			Handler: handler.profileTypes,
		},
		{
			Method:  http.MethodPost,
			Path:    "/flamegraph/querier.v1.QuerierService/LabelNames",
			Handler: handler.labelNames,
		},
		{
			Method:  http.MethodPost,
			Path:    "/flamegraph/querier.v1.QuerierService/LabelValues",
			Handler: handler.labelValues,
		},
	}
}

func handleProto[Request, Response any](
	ctx *server.Context,
	operation string,
	invoke func(context.Context, *Request) (*Response, error),
) error {
	request := new(Request)
	if err := ctx.ShouldBindBodyWith(request, binding.ProtoBuf); err != nil {
		return response.ErrInvalidRequest.WithMessage("invalid protobuf request")
	}
	result, err := invoke(ctx.Request().Context(), request)
	if err != nil {
		if errors.Is(err, profileservice.ErrInvalidQuery) {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
		if errors.Is(err, profileservice.ErrProfilesAbsent) {
			return response.ErrNotFound.WithMessage("profiles not found")
		}
		log.WithError(err).WithField("operation", operation).Error("profile query failed")
		return response.ErrInternal
	}
	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, result)
	return nil
}

func (h *queryHandler) selectMergeStacktraces(ctx *server.Context) error {
	return handleProto(ctx, "select_merge_stacktraces", h.service.SelectMergeStacktraces)
}

func (h *queryHandler) profileTypes(ctx *server.Context) error {
	return handleProto(ctx, "profile_types", h.service.ProfileTypes)
}

func (h *queryHandler) labelNames(ctx *server.Context) error {
	return handleProto(ctx, "label_names", h.service.LabelNames)
}

func (h *queryHandler) labelValues(ctx *server.Context) error {
	return handleProto(ctx, "label_values", h.service.LabelValues)
}
