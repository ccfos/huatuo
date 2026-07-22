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

package profiling

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/profiling"
)

const (
	ProfilingMemory = job.JobTypeProfilingMemory
	ProfilingCPU    = job.JobTypeProfilingCPU
)

// create creates a profiling job.
func (h *Handler) create(ctx *server.Context) error {
	req, err := parseCreateProfilingJobRequest(ctx)
	if err != nil {
		return response.ErrInvalidRequest
	}
	if req.Hostname == "" {
		return response.ErrInvalidRequest.WithMessage("hostname is required")
	}

	createReq, err := buildCreateProfilingJobRequest(
		req,
		ctx.UserID,
		&h.profilingConfig,
	)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	jobResult, err := h.jobManager.CreateContext(ctx.Request().Context(), createReq)
	if err != nil {
		if errors.Is(err, job.ErrQuotaExceeded) {
			return response.ErrConflict.WithMessage("profiling job quota exceeded")
		}
		log.WithError(err).Error("failed to create profiling job")
		return response.ErrInternal
	}
	response.Created(ctx, "/v1/profiles/"+jobResult.ID, v1.CreateProfilingJobResponse{
		ID: jobResult.ID,
	})
	return nil
}

// patchOne stops a profiling job. Body must be {"status":"stopped"}.
func (h *Handler) patchOne(ctx *server.Context) error {
	req, err := parsePatchProfilingJobRequest(ctx)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}
	taskID := req.ID

	jobResult, err := h.jobManager.GetByTypesContext(ctx.Request().Context(), taskID, ProfilingCPU, ProfilingMemory)
	if err != nil {
		if errors.Is(err, job.ErrNotFound) {
			return response.ErrNotFound
		}
		log.WithError(err).WithField("job_id", taskID).Error("failed to get profiling job")
		return response.ErrInternal
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}

	if jobResult.Status != job.JobStatusPending && jobResult.Status != job.JobStatusRunning {
		return response.ErrInvalidRequest.WithMessage("job already completed")
	}

	if err := h.jobManager.StopByTypesContext(ctx.Request().Context(), taskID, false, ProfilingCPU, ProfilingMemory); err != nil {
		log.WithError(err).WithField("job_id", taskID).Error("failed to stop profiling job")
		return response.ErrInternal
	}

	response.Success(ctx, nil)
	return nil
}

// list lists profiling jobs based on filters.
func (h *Handler) list(ctx *server.Context) error {
	req, err := parseProfilingJobListRequest(ctx)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	req.JobQuery.Sort = req.ListParams.Sort
	req.JobQuery.Offset = req.ListParams.Offset
	req.JobQuery.Limit = req.ListParams.Limit
	page, err := h.jobManager.ListPageContext(
		ctx.Request().Context(), ctx.UserID, ctx.IsAdmin, &req.JobQuery,
	)
	if err != nil {
		if errors.Is(err, job.ErrInvalidQuery) {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
		log.WithError(err).Error("failed to list profiling jobs")
		return response.ErrInternal
	}

	items := make([]v1.ProfilingJobResponse, len(page.Items))
	for i, j := range page.Items {
		items[i], err = buildProfilingJobResponse(j, h.profilingConfig.FlameGraphBaseURL)
		if err != nil {
			log.WithError(err).WithField("job_id", j.ID).
				Error("failed to build profiling job response")
			return response.ErrInternal
		}
	}

	response.Success(ctx, v1.ProfilingJobListResponse{
		Items:  items,
		Total:  int(page.Total),
		Limit:  req.ListParams.Limit,
		Offset: req.ListParams.Offset,
	})
	return nil
}

// get gets a specific profiling job by ID.
func (h *Handler) get(ctx *server.Context) error {
	taskID, err := parseProfilingJobID(ctx)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	jobResult, err := h.jobManager.GetByTypesContext(ctx.Request().Context(), taskID, ProfilingCPU, ProfilingMemory)
	if err != nil {
		if errors.Is(err, job.ErrNotFound) {
			return response.ErrNotFound
		}
		log.WithError(err).WithField("job_id", taskID).Error("failed to get profiling job")
		return response.ErrInternal
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}
	profilingResponse, err := buildProfilingJobResponse(jobResult, h.profilingConfig.FlameGraphBaseURL)
	if err != nil {
		log.WithError(err).WithField("job_id", taskID).
			Error("failed to build profiling job response")
		return response.ErrInternal
	}

	response.Success(ctx, profilingResponse)
	return nil
}

func buildProfilingJobResponse(jobResult *job.Job, flameGraphBaseURL string) (v1.ProfilingJobResponse, error) {
	profileType, err := profilingAPIType(jobResult.Type)
	if err != nil {
		return v1.ProfilingJobResponse{}, err
	}

	resultURL := jobResult.Result.URL
	if resultURL == "" && profilingJobHasResults(jobResult.Status) {
		resultURL = getFlameGraphURL(flameGraphBaseURL, jobResult)
	}

	privateData, err := decodeProfilingPrivateData(jobResult.PrivateData)
	if err != nil {
		return v1.ProfilingJobResponse{}, err
	}
	if privateData.Duration == 0 {
		privateData.Duration = jobResult.AgentTask.Duration / 2
	}
	resp := v1.ProfilingJobResponse{
		ID:          jobResult.ID,
		AgentTaskID: jobResult.AgentTaskID,
		ContainerID: jobResult.ContainerID,
		Hostname:    jobResult.Hostname,
		Status:      string(jobResult.Status),
		Type:        profileType,
		StartTime:   formatProfilingTime(jobResult.StartTime),
		EndTime:     formatProfilingTime(jobResult.EndTime),
		TracerArgs:  jobResult.AgentTask.TracerArgs,
		Duration:    privateData.Duration,
		Results: v1.ProfilingResults{
			URL: resultURL,
		},
		ErrorMessage:    jobResult.ErrorMessage,
		MemoryMode:      privateData.MemoryMode,
		BinaryMatchPath: privateData.BinaryMatchPath,
		Language:        privateData.Language,
	}

	return resp, nil
}

func isProfilingJobType(jobType job.JobType) bool {
	return jobType == ProfilingCPU || jobType == ProfilingMemory
}

func profilingAPIType(jobType job.JobType) (string, error) {
	switch jobType {
	case ProfilingMemory:
		return string(profiling.TypeMemory), nil
	case ProfilingCPU:
		return string(profiling.TypeCPU), nil
	default:
		return "", fmt.Errorf("job %q is not a profiling job", jobType)
	}
}

func profilingJobHasResults(status job.JobStatus) bool {
	return status == job.JobStatusCompleted || status == job.JobStatusStopped
}

func decodeProfilingPrivateData(data json.RawMessage) (profilingJobPrivateData, error) {
	if len(data) == 0 {
		return profilingJobPrivateData{}, nil
	}

	var privateData profilingJobPrivateData
	if err := json.Unmarshal(data, &privateData); err != nil {
		return profilingJobPrivateData{}, fmt.Errorf("decoding profiling private data: %w", err)
	}
	return privateData, nil
}

func formatProfilingTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}

func getFlameGraphURL(base string, jobResult *job.Job) string {
	var dashboardUID string
	var dashboardSlug string
	var labelKey string
	var labelVal string

	from := jobResult.StartTime.UTC().Format("2006-01-02T15:04:05.000Z")
	to := jobResult.EndTime.UTC().Format("2006-01-02T15:04:05.000Z")

	if jobResult.ContainerID != "" {
		switch jobResult.Type {
		case ProfilingMemory:
			dashboardUID = "container-memory-profiling"
			dashboardSlug = "e5aeb9-e599a8-memory-profiling"
		case ProfilingCPU:
			dashboardUID = "container-cpu-profiling"
			dashboardSlug = "e5aeb9-e599a8-cpu-profiling"
		default:
			return ""
		}
		labelKey = "var-container_id"
		labelVal = jobResult.ContainerID
	} else {
		switch jobResult.Type {
		case ProfilingMemory:
			dashboardUID = "host-memory-profiling"
			dashboardSlug = "e5aebf-e4b8bb-e69cba-memory-profiling"
		case ProfilingCPU:
			dashboardUID = "host-cpu-profiling"
			dashboardSlug = "e5aebf-e4b8bb-e69cba-cpu-profiling"
		default:
			return ""
		}
		labelKey = "var-hostname"
		labelVal = jobResult.Hostname
	}

	query := url.Values{}
	query.Set("orgId", "1")
	query.Set("from", from)
	query.Set("to", to)
	query.Set("timezone", "browser")
	query.Set(labelKey, labelVal)

	return fmt.Sprintf("%s/%s/%s?%s", base, dashboardUID, dashboardSlug, query.Encode())
}

// delete deletes a profiling job record by ID.
func (h *Handler) delete(ctx *server.Context) error {
	taskID, err := parseProfilingJobID(ctx)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	jobResult, err := h.jobManager.GetByTypesContext(ctx.Request().Context(), taskID, ProfilingCPU, ProfilingMemory)
	if err != nil {
		if errors.Is(err, job.ErrNotFound) {
			return response.ErrNotFound
		}
		log.WithError(err).WithField("job_id", taskID).Error("failed to get profiling job")
		return response.ErrInternal
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}

	if err := h.jobManager.DeleteByTypesContext(ctx.Request().Context(), taskID, ProfilingCPU, ProfilingMemory); err != nil {
		if errors.Is(err, job.ErrCannotDeleteRunning) {
			return response.ErrConflict.WithMessage("cannot delete running job")
		}
		log.WithError(err).WithField("job_id", taskID).Error("failed to delete profiling job")
		return response.ErrInternal
	}

	response.NoContent(ctx)
	return nil
}

// getRawData gets raw profiling data from ES by job ID.
func (h *Handler) getRawData(ctx *server.Context) error {
	listParams, err := ctx.ParseListParams()
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}
	taskID, err := parseProfilingJobID(ctx)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	jobResult, err := h.jobManager.GetByTypesContext(ctx.Request().Context(), taskID, ProfilingCPU, ProfilingMemory)
	if err != nil {
		if errors.Is(err, job.ErrNotFound) {
			return response.ErrNotFound
		}
		log.WithError(err).WithField("job_id", taskID).Error("failed to get profiling job")
		return response.ErrInternal
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}

	if jobResult.AgentTaskID == "" {
		return response.ErrInvalidRequest.WithMessage("agent job ID not found")
	}

	if h.profileService == nil {
		return response.ErrInternal
	}
	profiles, err := h.profileService.GetProfilesByTracerIDPage(
		ctx.Request().Context(), jobResult.AgentTaskID, listParams.Limit+1, listParams.Offset,
	)
	if err != nil {
		log.WithError(err).WithField("job_id", taskID).Error("failed to get raw profiling data")
		return response.ErrInternal
	}

	hasMore := len(profiles) > listParams.Limit
	if hasMore {
		profiles = profiles[:listParams.Limit]
	}
	response.Success(ctx, v1.RawDataResponse{
		Data:    profiles,
		Limit:   listParams.Limit,
		Offset:  listParams.Offset,
		HasMore: hasMore,
	})
	return nil
}
