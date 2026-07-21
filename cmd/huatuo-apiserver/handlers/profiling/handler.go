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

	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/job"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"
)

// Handler handles profiling-related HTTP requests.
type Handler struct {
	jobManager      jobManager
	profileService  *profileService.Service
	profilingConfig config.ProfilingConfig
	Handlers        []server.Handle
}

type jobManager interface {
	CreateContext(ctx context.Context, request *job.CreateJobRequest) (*job.Job, error)
	ListContext(ctx context.Context, userID string, isAdmin bool, query *job.JobQuery) ([]*job.Job, error)
	GetByTypesContext(ctx context.Context, jobID string, expectedTypes ...string) (*job.Job, error)
	StopByTypesContext(ctx context.Context, jobID string, force bool, expectedTypes ...string) error
	DeleteByTypesContext(ctx context.Context, jobID string, expectedTypes ...string) error
}

// NewHandler creates a new profiling handler.
func NewHandler(
	jm jobManager,
	profileSvc *profileService.Service,
	configs ...config.ProfilingConfig,
) *Handler {
	h := &Handler{
		jobManager:     jm,
		profileService: profileSvc,
	}
	if len(configs) > 0 {
		h.profilingConfig = configs[0]
	} else {
		h.profilingConfig = config.Get().Profiling
	}

	h.Handlers = []server.Handle{
		{Typ: server.HttpGet, Uri: "/capabilities", Handle: h.capabilities},
		{Typ: server.HttpPost, Uri: "", Handle: h.create},
		{Typ: server.HttpGet, Uri: "", Handle: h.list},
		{Typ: server.HttpGet, Uri: "/:id", Handle: h.get},
		{Typ: server.HttpGet, Uri: "/:id/raw", Handle: h.getRawData},
		{Typ: server.HttpPatch, Uri: "/:id", Handle: h.patchOne},
		{Typ: server.HttpDelete, Uri: "/:id", Handle: h.delete},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/SelectMergeStacktraces", Handle: h.displaySelectMergeStacktraces},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/ProfileTypes", Handle: h.displayProfileTypes},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/SelectSeries", Handle: h.displaySelectSeries},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/LabelNames", Handle: h.displayLabelNames},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/LabelValues", Handle: h.displayLabelValues},
	}

	return h
}
