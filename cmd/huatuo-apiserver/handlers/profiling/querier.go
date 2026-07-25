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
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"huatuo-bamai/internal/log"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"

	"github.com/gin-gonic/gin/binding"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
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
		status, message := profileQueryHTTPError(err)
		if status == http.StatusInternalServerError {
			log.WithError(err).WithField("operation", operation).Error("profile query failed")
		}
		ctx.JSON(status, map[string]any{"message": message})
		return nil
	}

	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, resp)
	return nil
}

func profileQueryHTTPError(err error) (int, string) {
	switch {
	case errors.Is(err, profileService.ErrInvalidQuery):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, profileService.ErrProfilesAbsent):
		return http.StatusNotFound, "profiles not found"
	case errors.Is(err, profileService.ErrProfileQueryLimitExceeded):
		return http.StatusUnprocessableEntity, err.Error()
	default:
		return http.StatusInternalServerError, "internal error"
	}
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

func (h *Handler) displayDiff(ctx *server.Context) error {
	return handleProto(ctx, "diff", h.profileService.Diff)
}

func (h *Handler) displayLabelNames(ctx *server.Context) error {
	return handleProto(ctx, "label_names", h.profileService.LabelNames)
}

func (h *Handler) displayLabelValues(ctx *server.Context) error {
	return handleProto(ctx, "label_values", h.profileService.LabelValues)
}

func profileExportRequest(
	query func(string) string,
) (*querierv1.SelectMergeStacktracesRequest, error) {
	profileType := strings.TrimSpace(query("profile_type"))
	if profileType == "" {
		return nil, errors.New("profile_type is required")
	}
	selector := strings.TrimSpace(query("selector"))
	if selector == "" {
		return nil, errors.New("selector is required")
	}
	start, err := strconv.ParseInt(query("start"), 10, 64)
	if err != nil {
		return nil, errors.New("start must be a Unix timestamp in milliseconds")
	}
	end, err := strconv.ParseInt(query("end"), 10, 64)
	if err != nil {
		return nil, errors.New("end must be a Unix timestamp in milliseconds")
	}
	return &querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: profileType,
		LabelSelector: selector,
		Start:         start,
		End:           end,
	}, nil
}

func writeProfileServiceError(ctx *server.Context, operation string, err error) {
	status, message := profileQueryHTTPError(err)
	if status == http.StatusInternalServerError {
		log.WithError(err).WithField("operation", operation).Error("profile query failed")
	}
	ctx.JSON(status, map[string]any{"message": message})
}

func (h *Handler) displayPprofExport(ctx *server.Context) error {
	req, err := profileExportRequest(ctx.Query)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil
	}
	payload, err := h.profileService.MarshalPprof(ctx.Request().Context(), req)
	if err != nil {
		writeProfileServiceError(ctx, "export_pprof", err)
		return nil
	}
	ctx.Header("Content-Type", "application/octet-stream")
	ctx.Header(
		"Content-Disposition",
		`attachment; filename="huatuo-profile.pb.gz"`,
	)
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.Writer().WriteHeader(http.StatusOK)
	if _, err := ctx.Writer().Write(payload); err != nil {
		return fmt.Errorf("write pprof export: %w", err)
	}
	return nil
}

func (h *Handler) displaySVGExport(ctx *server.Context) error {
	req, err := profileExportRequest(ctx.Query)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil
	}
	var output bytes.Buffer
	if err := h.profileService.RenderProfileSVG(
		ctx.Request().Context(),
		req,
		&output,
	); err != nil {
		writeProfileServiceError(ctx, "export_svg", err)
		return nil
	}
	ctx.Header("Content-Type", "image/svg+xml; charset=utf-8")
	ctx.Header(
		"Content-Disposition",
		`inline; filename="huatuo-flamegraph.svg"`,
	)
	ctx.Header(
		"Content-Security-Policy",
		"sandbox allow-scripts; default-src 'none'; "+
			"script-src 'unsafe-inline'; style-src 'unsafe-inline'",
	)
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.Writer().WriteHeader(http.StatusOK)
	if _, err := ctx.Writer().Write(output.Bytes()); err != nil {
		return fmt.Errorf("write SVG export: %w", err)
	}
	return nil
}
