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

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	serverapi "huatuo-bamai/apis/v1/server"
	profilinghandler "huatuo-bamai/cmd/huatuo-apiserver/handlers/profiling"
	tracehandler "huatuo-bamai/cmd/huatuo-apiserver/handlers/trace"
	"huatuo-bamai/internal/auth"
	"huatuo-bamai/internal/job"
	profilingresult "huatuo-bamai/internal/profiling/result"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"
	tracingdomain "huatuo-bamai/pkg/tracing"
)

// APIHandler implements the generated Apiserver Strict Server.
type APIHandler struct {
	profiling *profilinghandler.Service
	tracing   *tracehandler.Service
	openAPI   serverapi.GetOpenAPI200JSONResponse
}

var _ serverapi.StrictServerInterface = (*APIHandler)(nil)

// NewAPIHandler constructs the generated Server API adapter.
func NewAPIHandler(
	profilingService *profilinghandler.Service,
	tracingService *tracehandler.Service,
) (*APIHandler, error) {
	if profilingService == nil {
		return nil, errors.New("create Server API handler: Profiling service is required")
	}
	if tracingService == nil {
		return nil, errors.New("create Server API handler: Tracing service is required")
	}
	var specification map[string]any
	if err := json.Unmarshal(serverapi.OpenAPIJSON(), &specification); err != nil {
		return nil, fmt.Errorf("create Server API handler: decode bundled OpenAPI: %w", err)
	}
	return &APIHandler{
		profiling: profilingService,
		tracing:   tracingService,
		openAPI:   specification,
	}, nil
}

// GetOpenAPI returns the bundled Server API protocol document.
func (h *APIHandler) GetOpenAPI(
	context.Context,
	serverapi.GetOpenAPIRequestObject,
) (serverapi.GetOpenAPIResponseObject, error) {
	return h.openAPI, nil
}

// CreateProfilingJob creates one independent Profiling Job.
func (h *APIHandler) CreateProfilingJob(
	ctx context.Context,
	request serverapi.CreateProfilingJobRequestObject,
) (serverapi.CreateProfilingJobResponseObject, error) {
	principal, err := requestPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, response.ErrInvalidRequest.WithMessage("request body is required")
	}
	containerID := optionalValue(request.Body.ContainerID)
	binaryMatchPath := optionalValue(request.Body.BinaryMatchPath)
	jobEntity, err := h.profiling.Create(ctx, principal, &profilinghandler.CreateInput{
		Hostname:        request.Body.Hostname,
		DurationSeconds: request.Body.DurationSeconds,
		Scope:           observation.Scope(request.Body.Scope),
		ContainerID:     containerID,
		Spec: profilingdomain.Spec{
			Type:            profilingdomain.Type(request.Body.Type),
			Language:        profilingdomain.Language(request.Body.Language),
			Mode:            profilingdomain.Mode(request.Body.Mode),
			BinaryMatchPath: binaryMatchPath,
		},
	})
	if err != nil {
		return nil, serverAPIError(err)
	}
	payload, err := h.profilingJob(ctx, principal, jobEntity, false)
	if err != nil {
		return nil, err
	}
	return serverapi.CreateProfilingJob201JSONResponse{
		Body: serverapi.ProfilingJobResponse{Data: payload},
		Headers: serverapi.CreateProfilingJob201ResponseHeaders{
			Location: "/v1/profiling/" + jobEntity.ID,
		},
	}, nil
}

// ListProfilingJobs lists authorized Profiling Jobs.
func (h *APIHandler) ListProfilingJobs(
	ctx context.Context,
	request serverapi.ListProfilingJobsRequestObject,
) (serverapi.ListProfilingJobsResponseObject, error) {
	principal, err := requestPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	limit, offset := profilinghandler.NormalizePage(request.Params.Limit, request.Params.Offset)
	page, err := h.profiling.List(ctx, principal, limit, offset)
	if err != nil {
		return nil, serverAPIError(err)
	}
	result := serverapi.ProfilingJobListResponse{}
	result.Data.Items = make([]serverapi.ProfilingJob, len(page.Items))
	for i, jobEntity := range page.Items {
		result.Data.Items[i], err = h.profilingJob(ctx, principal, jobEntity, false)
		if err != nil {
			return nil, err
		}
	}
	result.Data.Total = int(page.Total)
	result.Data.Limit = limit
	result.Data.Offset = offset
	return serverapi.ListProfilingJobs200JSONResponse(result), nil
}

// GetProfilingCapabilities returns static product capabilities.
func (h *APIHandler) GetProfilingCapabilities(
	ctx context.Context,
	_ serverapi.GetProfilingCapabilitiesRequestObject,
) (serverapi.GetProfilingCapabilitiesResponseObject, error) {
	if _, err := requestPrincipal(ctx); err != nil {
		return nil, err
	}
	capabilities := h.profiling.Capabilities()
	items := make([]serverapi.ProfilingCapability, len(capabilities))
	for i := range capabilities {
		items[i] = profilingCapability(&capabilities[i])
	}
	return serverapi.GetProfilingCapabilities200JSONResponse(
		serverapi.ProfilingCapabilitiesResponse{
			Data: serverapi.ProfilingCapabilities{Items: items},
		},
	), nil
}

// GetProfilingJob returns one authorized Profiling Job.
func (h *APIHandler) GetProfilingJob(
	ctx context.Context,
	request serverapi.GetProfilingJobRequestObject,
) (serverapi.GetProfilingJobResponseObject, error) {
	principal, err := requestPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	jobEntity, err := h.profiling.Get(ctx, principal, request.RequestID)
	if err != nil {
		return nil, serverAPIError(err)
	}
	payload, err := h.profilingJob(ctx, principal, jobEntity, true)
	if err != nil {
		return nil, err
	}
	return serverapi.GetProfilingJob200JSONResponse(
		serverapi.ProfilingJobResponse{Data: payload},
	), nil
}

// StopProfilingJob records one asynchronous user stop intent.
func (h *APIHandler) StopProfilingJob(
	ctx context.Context,
	request serverapi.StopProfilingJobRequestObject,
) (serverapi.StopProfilingJobResponseObject, error) {
	principal, err := requestPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	jobEntity, err := h.profiling.Stop(ctx, principal, request.RequestID)
	if err != nil {
		return nil, serverAPIError(err)
	}
	payload, err := h.profilingJob(ctx, principal, jobEntity, false)
	if err != nil {
		return nil, err
	}
	return serverapi.StopProfilingJob200JSONResponse(
		serverapi.ProfilingJobResponse{Data: payload},
	), nil
}

// GetRawProfiles returns one authorized published result page.
func (h *APIHandler) GetRawProfiles(
	ctx context.Context,
	request serverapi.GetRawProfilesRequestObject,
) (serverapi.GetRawProfilesResponseObject, error) {
	principal, err := requestPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	limit, offset := profilinghandler.NormalizePage(request.Params.Limit, request.Params.Offset)
	page, err := h.profiling.RawProfiles(
		ctx,
		principal,
		request.RequestID,
		limit,
		offset,
	)
	if err != nil {
		return nil, serverAPIError(err)
	}
	items := make([]serverapi.RawProfile, len(page.Items))
	for i := range page.Items {
		items[i], err = rawProfile(&page.Items[i])
		if err != nil {
			return nil, err
		}
	}
	return serverapi.GetRawProfiles200JSONResponse(serverapi.RawProfilePageResponse{
		Data: serverapi.RawProfilePage{
			Items:   items,
			Limit:   page.Limit,
			Offset:  page.Offset,
			HasMore: page.HasMore,
		},
	}), nil
}

// CreateTracingJob creates one independent Tracing Job.
func (h *APIHandler) CreateTracingJob(
	ctx context.Context,
	request serverapi.CreateTracingJobRequestObject,
) (serverapi.CreateTracingJobResponseObject, error) {
	principal, err := requestPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, response.ErrInvalidRequest.WithMessage("request body is required")
	}
	jobEntity, err := h.tracing.Create(ctx, principal, tracehandler.CreateInput{
		Hostname:        request.Body.Hostname,
		DurationSeconds: request.Body.DurationSeconds,
		Scope:           observation.Scope(request.Body.Scope),
		ContainerID:     optionalValue(request.Body.ContainerID),
		Spec: tracingdomain.Spec{
			Type: tracingdomain.Type(request.Body.Type),
		},
	})
	if err != nil {
		return nil, serverAPIError(err)
	}
	payload, err := tracingJob(jobEntity)
	if err != nil {
		return nil, err
	}
	return serverapi.CreateTracingJob201JSONResponse{
		Body: serverapi.TracingJobResponse{Data: payload},
		Headers: serverapi.CreateTracingJob201ResponseHeaders{
			Location: "/v1/tracing/" + jobEntity.ID,
		},
	}, nil
}

// ListTracingJobs lists authorized Tracing Jobs.
func (h *APIHandler) ListTracingJobs(
	ctx context.Context,
	request serverapi.ListTracingJobsRequestObject,
) (serverapi.ListTracingJobsResponseObject, error) {
	principal, err := requestPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	limit, offset := tracehandler.NormalizePage(request.Params.Limit, request.Params.Offset)
	page, err := h.tracing.List(ctx, principal, limit, offset)
	if err != nil {
		return nil, serverAPIError(err)
	}
	result := serverapi.TracingJobListResponse{}
	result.Data.Items = make([]serverapi.TracingJob, len(page.Items))
	for i, jobEntity := range page.Items {
		result.Data.Items[i], err = tracingJob(jobEntity)
		if err != nil {
			return nil, err
		}
	}
	result.Data.Total = int(page.Total)
	result.Data.Limit = limit
	result.Data.Offset = offset
	return serverapi.ListTracingJobs200JSONResponse(result), nil
}

// GetTracingCapabilities returns static product capabilities.
func (h *APIHandler) GetTracingCapabilities(
	ctx context.Context,
	_ serverapi.GetTracingCapabilitiesRequestObject,
) (serverapi.GetTracingCapabilitiesResponseObject, error) {
	if _, err := requestPrincipal(ctx); err != nil {
		return nil, err
	}
	capabilities := h.tracing.Capabilities()
	items := make([]serverapi.TracingCapability, len(capabilities))
	for i := range capabilities {
		items[i] = tracingCapability(&capabilities[i])
	}
	return serverapi.GetTracingCapabilities200JSONResponse(
		serverapi.TracingCapabilitiesResponse{
			Data: serverapi.TracingCapabilities{Items: items},
		},
	), nil
}

// GetTracingJob returns one authorized Tracing Job.
func (h *APIHandler) GetTracingJob(
	ctx context.Context,
	request serverapi.GetTracingJobRequestObject,
) (serverapi.GetTracingJobResponseObject, error) {
	principal, err := requestPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	jobEntity, err := h.tracing.Get(ctx, principal, request.RequestID)
	if err != nil {
		return nil, serverAPIError(err)
	}
	payload, err := tracingJob(jobEntity)
	if err != nil {
		return nil, err
	}
	return serverapi.GetTracingJob200JSONResponse(
		serverapi.TracingJobResponse{Data: payload},
	), nil
}

// StopTracingJob records one asynchronous user stop intent.
func (h *APIHandler) StopTracingJob(
	ctx context.Context,
	request serverapi.StopTracingJobRequestObject,
) (serverapi.StopTracingJobResponseObject, error) {
	principal, err := requestPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	jobEntity, err := h.tracing.Stop(ctx, principal, request.RequestID)
	if err != nil {
		return nil, serverAPIError(err)
	}
	payload, err := tracingJob(jobEntity)
	if err != nil {
		return nil, err
	}
	return serverapi.StopTracingJob200JSONResponse(
		serverapi.TracingJobResponse{Data: payload},
	), nil
}

func (h *APIHandler) profilingJob(
	ctx context.Context,
	principal auth.Principal,
	jobEntity *job.Job,
	includeResultURL bool,
) (serverapi.ProfilingJob, error) {
	if jobEntity == nil || jobEntity.Kind != job.KindProfiling || jobEntity.Spec.Profiling == nil {
		return serverapi.ProfilingJob{}, errors.New("map Profiling Job: invalid domain Job")
	}
	common := commonJob(jobEntity)
	var resultURL *string
	if includeResultURL {
		var err error
		resultURL, err = h.profiling.ResultURL(ctx, principal, jobEntity)
		if err != nil {
			return serverapi.ProfilingJob{}, serverAPIError(err)
		}
	}
	return serverapi.ProfilingJob{
		RequestID:       common.RequestID,
		Hostname:        common.Hostname,
		DurationSeconds: common.DurationSeconds,
		Scope:           common.Scope,
		ContainerID:     common.ContainerID,
		Status:          common.Status,
		Failure:         common.Failure,
		CreatedAt:       common.CreatedAt,
		UpdatedAt:       common.UpdatedAt,
		StartedAt:       common.StartedAt,
		EndedAt:         common.EndedAt,
		ResultURL:       resultURL,
		Type:            serverapi.ProfilingType(jobEntity.Spec.Profiling.Type),
		Language:        serverapi.ProfilingLanguage(jobEntity.Spec.Profiling.Language),
		Mode:            serverapi.ProfilingMode(jobEntity.Spec.Profiling.Mode),
		BinaryMatchPath: optionalString(jobEntity.Spec.Profiling.BinaryMatchPath),
	}, nil
}

func tracingJob(jobEntity *job.Job) (serverapi.TracingJob, error) {
	if jobEntity == nil || jobEntity.Kind != job.KindTracing || jobEntity.Spec.Tracing == nil {
		return serverapi.TracingJob{}, errors.New("map Tracing Job: invalid domain Job")
	}
	common := commonJob(jobEntity)
	return serverapi.TracingJob{
		RequestID:       common.RequestID,
		Hostname:        common.Hostname,
		DurationSeconds: common.DurationSeconds,
		Scope:           common.Scope,
		ContainerID:     common.ContainerID,
		Status:          common.Status,
		Failure:         common.Failure,
		CreatedAt:       common.CreatedAt,
		UpdatedAt:       common.UpdatedAt,
		StartedAt:       common.StartedAt,
		EndedAt:         common.EndedAt,
		Type:            serverapi.TracingType(jobEntity.Spec.Tracing.Type),
	}, nil
}

func commonJob(jobEntity *job.Job) serverapi.Job {
	result := serverapi.Job{
		RequestID:       jobEntity.ID,
		Hostname:        jobEntity.Hostname,
		DurationSeconds: int64(jobEntity.Duration.Seconds()),
		Scope:           apiv1.ObservationScope(jobEntity.Scope),
		ContainerID:     optionalString(jobEntity.ContainerID),
		Status:          serverapi.JobStatus(jobEntity.Status),
		CreatedAt:       jobEntity.CreatedAt,
		UpdatedAt:       jobEntity.UpdatedAt,
		StartedAt:       optionalTime(jobEntity.StartedAt),
		EndedAt:         optionalTime(jobEntity.EndedAt),
	}
	if jobEntity.Failure != nil {
		result.Failure = &serverapi.JobFailure{
			Code:    apiv1.ErrorCode(jobEntity.Failure.Reason),
			Message: jobEntity.Failure.Message,
		}
	}
	return result
}

func profilingCapability(capability *profilingdomain.Capability) serverapi.ProfilingCapability {
	modes := make([]serverapi.ProfilingMode, len(capability.Modes))
	for i, mode := range capability.Modes {
		modes[i] = serverapi.ProfilingMode(mode)
	}
	scopes := make([]apiv1.ObservationScope, len(capability.SupportedScopes))
	for i, scope := range capability.SupportedScopes {
		scopes[i] = apiv1.ObservationScope(scope)
	}
	return serverapi.ProfilingCapability{
		Type:                serverapi.ProfilingType(capability.Type),
		Language:            serverapi.ProfilingLanguage(capability.Language),
		Modes:               modes,
		SupportsBinaryMatch: capability.SupportsBinaryMatch,
		SupportedScopes:     scopes,
	}
}

func tracingCapability(capability *tracingdomain.Capability) serverapi.TracingCapability {
	scopes := make([]apiv1.ObservationScope, len(capability.SupportedScopes))
	for i, scope := range capability.SupportedScopes {
		scopes[i] = apiv1.ObservationScope(scope)
	}
	return serverapi.TracingCapability{
		Type:            serverapi.TracingType(capability.Type),
		SupportedScopes: scopes,
	}
}

func rawProfile(profile *profilingresult.Profile) (serverapi.RawProfile, error) {
	data, err := json.Marshal(profile.Profile)
	if err != nil {
		return serverapi.RawProfile{}, fmt.Errorf("map raw Profile: encode payload: %w", err)
	}
	return serverapi.RawProfile{
		Hostname:          profile.Hostname,
		Region:            profile.Region,
		UploadedAt:        profile.UploadedAt,
		CapturedAt:        profile.CapturedAt,
		ContainerID:       optionalString(profile.ContainerID),
		ContainerHostname: optionalString(profile.ContainerHostname),
		ContainerType:     optionalString(profile.ContainerType),
		ContainerQos:      optionalString(profile.ContainerQOS),
		ProfileType:       profile.ProfileType,
		Profile:           json.RawMessage(data),
	}, nil
}

func requestPrincipal(ctx context.Context) (auth.Principal, error) {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok || principal.ID == "" {
		return auth.Principal{}, response.NewAPIError(
			apiv1.ErrorCodeUnauthenticated,
			"authentication is required",
		)
	}
	return principal, nil
}

func serverAPIError(err error) error {
	switch {
	case errors.Is(err, job.ErrNotFound), errors.Is(err, profilingresult.ErrWrongKind):
		return response.NewAPIError(serverapi.ErrorCodeJobNotFound, "Job not found")
	case errors.Is(err, auth.ErrPermissionDenied), errors.Is(err, profilingresult.ErrForbidden):
		return response.NewAPIError(apiv1.ErrorCodePermissionDenied, "Job access is forbidden")
	case errors.Is(err, job.ErrQuotaExceeded):
		return response.NewAPIError(serverapi.ErrorCodeQuotaExceeded, "Job quota exceeded")
	case errors.Is(err, job.ErrJobTerminal), errors.Is(err, job.ErrConflict):
		return response.NewAPIError(serverapi.ErrorCodeJobConflict, "Job is already terminal")
	case errors.Is(err, job.ErrInvalidQuery):
		return response.ErrInvalidRequest.WithMessage(err.Error())
	case errors.Is(err, job.ErrShuttingDown):
		return response.NewAPIError(apiv1.ErrorCodeServiceUnavailable, "Job service is shutting down")
	case errors.Is(err, profilingresult.ErrNotReady):
		return response.NewAPIError(serverapi.ErrorCodeResultNotReady, "Profiling result is not ready")
	case errors.Is(err, profilingresult.ErrUnavailable):
		return response.NewAPIError(
			serverapi.ErrorCodeResultUnavailable,
			"Job state does not provide a complete Profiling result",
		)
	case errors.Is(err, profilingresult.ErrRepositoryUnavailable):
		return response.NewAPIError(
			apiv1.ErrorCodeServiceUnavailable,
			"Profiling result Store is unavailable",
		)
	default:
		return err
	}
}

func optionalValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	result := value
	return &result
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	result := value
	return &result
}
