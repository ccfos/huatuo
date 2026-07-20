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
	"net/http"

	"huatuo-bamai/internal/log"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"

	"github.com/gin-gonic/gin/binding"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
)

// DisplaySelectMergeStacktraces handles /querier.v1.QuerierService/SelectMergeStacktraces.
func (h *Handler) DisplaySelectMergeStacktraces(ctx *server.Context) error {
	req := &querierv1.SelectMergeStacktracesRequest{}
	if err := ctx.ShouldBindBodyWith(req, binding.ProtoBuf); err != nil {
		ctx.JSON(http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil
	}

	log.WithField("request", req).Debug("selecting merged stack traces")

	resp, err := profileService.SelectMergeStacktraces(req)
	if err != nil {
		log.WithError(err).Error("failed to select merged stack traces")
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": "internal error"})
		return nil
	}

	// fix internal: invalid content-type: "application/x-protobuf"; expecting "application/proto"
	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, resp)
	return nil
}

// DisplayProfileTypes handles /querier.v1.QuerierService/ProfileTypes.
func (h *Handler) DisplayProfileTypes(ctx *server.Context) error {
	req := &querierv1.ProfileTypesRequest{}
	if err := ctx.ShouldBindBodyWith(req, binding.ProtoBuf); err != nil {
		ctx.JSON(http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil
	}

	log.WithField("request", req).Debug("listing profile types")

	resp, err := profileService.ProfileTypes(req)
	if err != nil {
		log.WithError(err).Error("failed to list profile types")
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": "internal error"})
		return nil
	}

	// fix internal: invalid content-type: "application/x-protobuf"; expecting "application/proto"
	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, resp)
	return nil
}

// DisplaySelectSeries handles /querier.v1.QuerierService/SelectSeries.
func (h *Handler) DisplaySelectSeries(ctx *server.Context) error {
	req := &querierv1.SelectSeriesRequest{}
	if err := ctx.ShouldBindBodyWith(req, binding.ProtoBuf); err != nil {
		ctx.JSON(http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil
	}

	log.WithField("request", req).Debug("selecting profile series")

	resp, err := profileService.SelectSeries(req)
	if err != nil {
		log.WithError(err).Error("failed to select profile series")
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": "internal error"})
		return nil
	}

	// fix internal: invalid content-type: "application/x-protobuf"; expecting "application/proto"
	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, resp)
	return nil
}

// DisplayLabelNames handles /querier.v1.QuerierService/LabelNames.
func (h *Handler) DisplayLabelNames(ctx *server.Context) error {
	req := &typesv1.LabelNamesRequest{}
	if err := ctx.ShouldBindBodyWith(req, binding.ProtoBuf); err != nil {
		ctx.JSON(http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil
	}

	log.WithField("request", req).Debug("listing profile label names")

	resp, err := profileService.LabelNames(req)
	if err != nil {
		log.WithError(err).Error("failed to list profile label names")
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": "internal error"})
		return nil
	}

	// fix internal: invalid content-type: "application/x-protobuf"; expecting "application/proto"
	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, resp)
	return nil
}

// DisplayLabelValues handles /querier.v1.QuerierService/LabelValues.
func (h *Handler) DisplayLabelValues(ctx *server.Context) error {
	req := &typesv1.LabelValuesRequest{}
	if err := ctx.ShouldBindBodyWith(req, binding.ProtoBuf); err != nil {
		ctx.JSON(http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil
	}

	log.WithField("request", req).Debug("listing profile label values")

	resp, err := profileService.LabelValues(req)
	if err != nil {
		log.WithError(err).Error("failed to list profile label values")
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": "internal error"})
		return nil
	}

	// fix internal: invalid content-type: "application/x-protobuf"; expecting "application/proto"
	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, resp)
	return nil
}
