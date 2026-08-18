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

	"huatuo-bamai/internal/job"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
)

// Handler handles profiling-related HTTP requests.
type Handler struct {
	jobManager          JobManager
	profileQueryService ProfileQueryService
	profilingConfig     Config
	Handlers            []server.Route
}

// Config contains profiling values used by request and response handling.
type Config struct {
	AggregationIntervalSeconds     int
	MaxConcurrentProfilerProcesses int
	DashboardBaseURL               string
}

// ProfileQueryService defines profile query operations consumed by the handler.
type ProfileQueryService interface {
	SelectMergeStacktraces(ctx context.Context, req *querierv1.SelectMergeStacktracesRequest) (*querierv1.SelectMergeStacktracesResponse, error)
	ProfileTypes(ctx context.Context, req *querierv1.ProfileTypesRequest) (*querierv1.ProfileTypesResponse, error)
	LabelNames(ctx context.Context, req *typesv1.LabelNamesRequest) (*typesv1.LabelNamesResponse, error)
	LabelValues(ctx context.Context, req *typesv1.LabelValuesRequest) (*typesv1.LabelValuesResponse, error)
	GetProfilesByTracerIDPage(ctx context.Context, tracerID string, limit, offset int) ([]*profileService.ProfileDocument, error)
}

// JobManager defines the profiling handler's job operations.
type JobManager interface {
	CreateContext(ctx context.Context, request *job.CreateJobRequest) (*job.Job, error)
	ListPageContext(ctx context.Context, userID string, isAdmin bool, query *job.JobQuery) (*job.JobPage, error)
	GetByTypesContext(ctx context.Context, jobID string, expectedTypes ...job.JobType) (*job.Job, error)
	StopByTypesContext(ctx context.Context, jobID string, force bool, expectedTypes ...job.JobType) error
	DeleteByTypesContext(ctx context.Context, jobID string, expectedTypes ...job.JobType) error
}

// NewHandler creates a new profiling handler.
func NewHandler(
	jm JobManager,
	profileQueryService ProfileQueryService,
	profilingConfig Config,
) *Handler {
	h := &Handler{
		jobManager:          jm,
		profileQueryService: profileQueryService,
		profilingConfig:     profilingConfig,
	}

	h.Handlers = []server.Route{
		{Method: http.MethodGet, Path: "/capabilities", Handler: h.capabilities},
		{Method: http.MethodPost, Path: "", Handler: h.create},
		{Method: http.MethodGet, Path: "", Handler: h.list},
		{Method: http.MethodGet, Path: "/:id", Handler: h.get},
		{Method: http.MethodPatch, Path: "/:id", Handler: h.patchOne},
		{Method: http.MethodDelete, Path: "/:id", Handler: h.delete},
		{Method: http.MethodGet, Path: "/:id/raw", Handler: h.getRawData},
		{
			Method:  http.MethodPost,
			Path:    "/flamegraph/querier.v1.QuerierService/SelectMergeStacktraces",
			Handler: h.displaySelectMergeStacktraces,
		},
		{
			Method:  http.MethodPost,
			Path:    "/flamegraph/querier.v1.QuerierService/ProfileTypes",
			Handler: h.displayProfileTypes,
		},
		{
			Method:  http.MethodPost,
			Path:    "/flamegraph/querier.v1.QuerierService/LabelNames",
			Handler: h.displayLabelNames,
		},
		{
			Method:  http.MethodPost,
			Path:    "/flamegraph/querier.v1.QuerierService/LabelValues",
			Handler: h.displayLabelValues,
		},
	}

	return h
}
