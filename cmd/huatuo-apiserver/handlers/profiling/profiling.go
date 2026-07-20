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
	"strconv"
	"strings"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/listing"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/log"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/profiling"
)

const (
	ProfilingMemory = "profiling_memory"
	ProfilingCPU    = "profiling_cpu"
)

type profilingJobListQuery struct {
	ContainerID string `form:"containerID"`
	Hostname    string `form:"hostname"`
	Status      string `form:"status"`
	Type        string `form:"type"`
}

// create creates a profiling job.
func (h *Handler) create(ctx *server.Context) error {
	var req v1.CreateProfilingJobRequest

	if err := ctx.ShouldBindJSON(&req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	hasRunning, err := h.hasRunningProfilingJob(req.Hostname, ctx.UserID)
	if err != nil {
		return response.ErrInternal.WithMessage(err.Error())
	}
	if hasRunning {
		return response.ErrConflict.WithMessage("there is already a profiling job running on this host")
	}

	taskReq := job.AgentTaskRequest{
		TracerName:   "profiler",
		DataType:     "db-json",
		Interval:     h.profilingConfig.AggregationInterval,
		TraceTimeout: h.profilingConfig.ExecutionTimeout,
	}
	var jobType string
	switch req.ProfilingType {
	case string(profiling.TypeCPU):
		jobType = ProfilingCPU
		language, err := profiling.ParseLanguage(req.Language)
		if err != nil || !profiling.IsSupported(language, profiling.TypeCPU) {
			return response.ErrInvalidRequest.WithMessage(
				fmt.Sprintf("cpu profiling not supported for %q", req.Language),
			)
		}
		var typeArgs []string
		if req.BinaryMatchPath != "" {
			typeArgs = append(typeArgs, "--binary-match-path", req.BinaryMatchPath)
		}
		fillTracerArgs(&taskReq, profiling.TypeCPU, language, typeArgs...)
	case string(profiling.TypeMemory):
		jobType = ProfilingMemory
		language, err := profiling.ParseLanguage(req.Language)
		if err != nil || !profiling.IsSupported(language, profiling.TypeMemory) {
			return response.ErrInvalidRequest.WithMessage(
				fmt.Sprintf("memory profiling not supported for %q", req.Language),
			)
		}
		mode, err := profiling.ParseMemoryMode(strings.ToLower(req.MemoryMode))
		if err != nil || !profiling.SupportsMemoryMode(language, mode) {
			return response.ErrInvalidRequest.WithMessage(
				fmt.Sprintf("memory mode not supported: %q", req.MemoryMode),
			)
		}
		fillTracerArgs(
			&taskReq,
			profiling.TypeMemory,
			language,
			"--memory-mode", string(mode),
		)
	default:
		return response.ErrInvalidRequest.WithMessage(
			fmt.Sprintf("unsupported profiling type %q", req.ProfilingType),
		)
	}

	if req.Duration < taskReq.Interval*2 {
		return response.ErrInvalidRequest.WithMessage(
			"duration must cover at least two profiling intervals",
		)
	}
	if req.Duration+taskReq.Interval >= 3600 {
		return response.ErrInvalidRequest.WithMessage("duration plus profiling interval must be less than 3600 seconds")
	}
	if taskReq.TraceTimeout < req.Duration+taskReq.Interval {
		taskReq.TraceTimeout = req.Duration + taskReq.Interval
	}

	// profiling job need to be stopped from outside, so we need to set duration to args.Duration * 2,
	// job.Duration will control the actual profiling time
	taskReq.Duration = req.Duration * 2
	taskReq.TracerArgs = append(
		taskReq.TracerArgs,
		"--duration", strconv.Itoa(req.Duration),
		"--aggr-interval", strconv.Itoa(taskReq.Interval),
		"--max-concurrent-procs", strconv.Itoa(h.profilingConfig.MaxProfilerProcs),
		"--output-format", "remote",
		"--output-storage", "/var/run/huatuo-toolstream.sock",
	)

	privateData, err := newProfilingPrivateData(&req)
	if err != nil {
		log.WithError(err).Error("failed to encode profiling private data")
		return response.ErrInternal
	}

	jobResult, err := h.jobManager.Create(&job.CreateJobRequest{
		UserID:      ctx.UserID,
		ContainerID: req.ContainerID,
		Hostname:    req.Hostname,
		Type:        jobType,
		AgentTask:   &taskReq,
		PrivateData: privateData,
	})
	if err != nil {
		log.WithError(err).Error("failed to create profiling job")
		return response.ErrInternal
	}
	response.Created(ctx, "/v1/profiles/"+jobResult.ID, v1.CreateProfilingJobResponse{
		ID: jobResult.ID,
	})
	return nil
}

type profilingPrivateData struct {
	BinaryMatchPath string `json:"binary_match_path"`
	Duration        int    `json:"duration"`
	Language        string `json:"language"`
	MemoryMode      string `json:"memory_mode"`
}

func newProfilingPrivateData(req *v1.CreateProfilingJobRequest) (json.RawMessage, error) {
	data, err := json.Marshal(profilingPrivateData{
		BinaryMatchPath: req.BinaryMatchPath,
		Duration:        req.Duration,
		Language:        req.Language,
		MemoryMode:      req.MemoryMode,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding profiling private data: %w", err)
	}
	return data, nil
}

// hasRunningProfilingJob reports whether a profiling job is currently running on hostname for userID.
func (h *Handler) hasRunningProfilingJob(hostname, userID string) (bool, error) {
	filter := job.JobQuery{
		Hostname: hostname,
		Status:   "running",
	}
	jobs, err := h.jobManager.List(userID, false, &filter)
	if err != nil {
		return false, fmt.Errorf("listing running profiling jobs: %w", err)
	}
	if len(jobs) > 0 {
		return true, nil
	}
	return false, nil
}

func fillTracerArgs(
	agentTaskReq *job.AgentTaskRequest,
	profilingType profiling.Type,
	language profiling.Language,
	typeArgs ...string,
) {
	agentTaskReq.TracerArgs = append(
		agentTaskReq.TracerArgs,
		"-t", string(profilingType),
	)
	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, typeArgs...)
	agentTaskReq.TracerArgs = append(
		agentTaskReq.TracerArgs,
		"-l", string(language),
	)
}

// patchOne stops a profiling job. Body must be {"status":"stopped"}.
func (h *Handler) patchOne(ctx *server.Context) error {
	taskID := ctx.Param("id")
	if taskID == "" {
		return response.ErrInvalidRequest.WithMessage("id is required")
	}

	var req v1.PatchStatusRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}
	if req.Status != listing.StatusStopped {
		return response.ErrInvalidRequest.WithMessage(`status must be "stopped"`)
	}

	jobResult, err := h.jobManager.Get(taskID)
	if err != nil {
		return response.ErrNotFound.WithMessage(err.Error())
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}

	if jobResult.Status != job.JobStatusPending && jobResult.Status != job.JobStatusRunning {
		return response.ErrInvalidRequest.WithMessage("job already completed")
	}

	if err := h.jobManager.Stop(taskID, false); err != nil {
		log.WithError(err).WithField("job_id", taskID).Error("failed to stop profiling job")
		return response.ErrInternal
	}

	response.Success(ctx, nil)
	return nil
}

// list lists profiling jobs based on filters.
func (h *Handler) list(ctx *server.Context) error {
	listParams, err := ctx.ParseListParams()
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	queries, err := profilingJobQueries(ctx)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	var allJobs []*job.Job
	var listErr error
	for i := range queries {
		jobs, err := h.jobManager.List(ctx.UserID, ctx.IsAdmin, &queries[i])
		if err != nil {
			log.WithError(err).WithField("job_type", queries[i].Type).
				Error("failed to list profiling jobs")
			listErr = err
			continue
		}
		allJobs = append(allJobs, jobs...)
	}
	if listErr != nil && len(allJobs) == 0 {
		return response.ErrInternal
	}

	if err := listing.SortJobs(allJobs, listParams.Sort); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	total := len(allJobs)
	pageJobs := listing.Paginate(allJobs, listParams.Offset, listParams.Limit)

	items := make([]v1.ProfilingJobResponse, len(pageJobs))
	for i, j := range pageJobs {
		items[i], err = buildProfilingJobResponse(j, h.profilingConfig.FlameGraphBaseURL)
		if err != nil {
			log.WithError(err).WithField("job_id", j.ID).
				Error("failed to build profiling job response")
			return response.ErrInternal
		}
	}

	response.Success(ctx, v1.ProfilingJobListResponse{
		Items:  items,
		Total:  total,
		Limit:  listParams.Limit,
		Offset: listParams.Offset,
	})
	return nil
}

func profilingJobQueries(ctx *server.Context) ([]job.JobQuery, error) {
	var query profilingJobListQuery
	if err := ctx.ShouldBindQuery(&query); err != nil {
		return nil, fmt.Errorf("binding profiling job query: %w", err)
	}
	if err := validateProfilingJobStatus(query.Status); err != nil {
		return nil, err
	}

	switch query.Type {
	case "":
		return []job.JobQuery{
			{
				ContainerID: query.ContainerID,
				Hostname:    query.Hostname,
				Status:      query.Status,
				Type:        ProfilingMemory,
			},
			{
				ContainerID: query.ContainerID,
				Hostname:    query.Hostname,
				Status:      query.Status,
				Type:        ProfilingCPU,
			},
		}, nil
	case "cpu":
		return []job.JobQuery{
			{
				ContainerID: query.ContainerID,
				Hostname:    query.Hostname,
				Status:      query.Status,
				Type:        ProfilingCPU,
			},
		}, nil
	case "memory":
		return []job.JobQuery{
			{
				ContainerID: query.ContainerID,
				Hostname:    query.Hostname,
				Status:      query.Status,
				Type:        ProfilingMemory,
			},
		}, nil
	default:
		return nil, fmt.Errorf("invalid type %q", query.Type)
	}
}

func validateProfilingJobStatus(status string) error {
	switch job.JobStatus(status) {
	case "", job.JobStatusPending, job.JobStatusRunning, job.JobStatusCompleted,
		job.JobStatusFailed, job.JobStatusStopped, job.JobStatusTimeout:
		return nil
	default:
		return fmt.Errorf("invalid status %q", status)
	}
}

// get gets a specific profiling job by ID.
func (h *Handler) get(ctx *server.Context) error {
	taskID := ctx.Param("id")
	if taskID == "" {
		return response.ErrInvalidRequest.WithMessage("id is required")
	}

	jobResult, err := h.jobManager.Get(taskID)
	if err != nil {
		return response.ErrNotFound.WithMessage(err.Error())
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}
	if !isProfilingJobType(jobResult.Type) {
		return response.ErrNotFound.WithMessage(
			fmt.Sprintf("job %q is not a profiling job", taskID),
		)
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

func isProfilingJobType(jobType string) bool {
	return jobType == ProfilingCPU || jobType == ProfilingMemory
}

func profilingAPIType(jobType string) (string, error) {
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

func decodeProfilingPrivateData(data json.RawMessage) (profilingPrivateData, error) {
	if len(data) == 0 {
		return profilingPrivateData{}, nil
	}

	var privateData profilingPrivateData
	if err := json.Unmarshal(data, &privateData); err != nil {
		return profilingPrivateData{}, fmt.Errorf("decoding profiling private data: %w", err)
	}
	return privateData, nil
}

func formatProfilingTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("2006-01-02T15:04:05.000")
}

func getFlameGraphURL(base string, jobResult *job.Job) string {
	var dashboardUid string
	var dashboardSlug string
	var labelKey string
	var labelVal string

	from := jobResult.StartTime.UTC().Format("2006-01-02T15:04:05.000Z")
	to := jobResult.EndTime.UTC().Format("2006-01-02T15:04:05.000Z")

	if jobResult.ContainerID != "" {
		switch jobResult.Type {
		case ProfilingMemory:
			dashboardUid = "container-memory-profiling"
			dashboardSlug = "e5aeb9-e599a8-memory-profiling"
		case ProfilingCPU:
			dashboardUid = "container-cpu-profiling"
			dashboardSlug = "e5aeb9-e599a8-cpu-profiling"
		default:
			return ""
		}
		labelKey = "var-container_hostname"
		labelVal = jobResult.ContainerID
	} else {
		switch jobResult.Type {
		case ProfilingMemory:
			dashboardUid = "host-memory-profiling"
			dashboardSlug = "e5aebf-e4b8bb-e69cba-memory-profiling"
		case ProfilingCPU:
			dashboardUid = "host-cpu-profiling"
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

	return fmt.Sprintf("%s/%s/%s?%s", base, dashboardUid, dashboardSlug, query.Encode())
}

// delete deletes a profiling job record by ID.
func (h *Handler) delete(ctx *server.Context) error {
	taskID := ctx.Param("id")
	if taskID == "" {
		return response.ErrInvalidRequest.WithMessage("id is required")
	}

	jobResult, err := h.jobManager.Get(taskID)
	if err != nil {
		return response.ErrNotFound.WithMessage(err.Error())
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}

	if err := h.jobManager.Delete(taskID); err != nil {
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
	taskID := ctx.Param("id")
	if taskID == "" {
		return response.ErrInvalidRequest.WithMessage("id is required")
	}

	jobResult, err := h.jobManager.Get(taskID)
	if err != nil {
		return response.ErrNotFound.WithMessage(err.Error())
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}

	if jobResult.AgentTaskID == "" {
		return response.ErrInvalidRequest.WithMessage("agent job ID not found")
	}

	profiles, err := profileService.GetProfilesByTracerID(jobResult.AgentTaskID)
	if err != nil {
		log.WithError(err).WithField("job_id", taskID).Error("failed to get raw profiling data")
		return response.ErrInternal
	}

	response.Success(ctx, v1.RawDataResponse{
		Data: profiles,
	})
	return nil
}
