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

package trace

import (
	"errors"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/listing"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
)

// MaxTraceTimeout is the maximum allowed trace duration in seconds.
const MaxTraceTimeout = 300

// Handler handles trace-related HTTP requests.
type Handler struct {
	jobManager *job.Manager
	Handlers   []server.Handle
}

// NewHandler creates a new trace handler.
func NewHandler(jm *job.Manager) *Handler {
	h := &Handler{jobManager: jm}

	h.Handlers = []server.Handle{
		{Typ: server.HttpPost, Uri: "", Handle: h.start},
		{Typ: server.HttpGet, Uri: "", Handle: h.list},
		{Typ: server.HttpGet, Uri: "/:id", Handle: h.get},
		{Typ: server.HttpPatch, Uri: "", Handle: h.patchBulk},
		{Typ: server.HttpPatch, Uri: "/:id", Handle: h.patchOne},
		{Typ: server.HttpDelete, Uri: "/:id", Handle: h.delete},
	}
	return h
}

// start starts a new trace job.
func (h *Handler) start(ctx *server.Context) error {
	var req v1.CreateTraceJobRequest

	if err := ctx.ShouldBindJSON(&req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	args := job.NewAgentTaskReq{
		TracerName: "tracer",
		DataType:   "db",
	}

	if req.Type != "tracing" {
		args.TracerName = req.Type
	}

	if req.Duration > MaxTraceTimeout {
		args.TraceTimeout = MaxTraceTimeout
	} else {
		args.TraceTimeout = req.Duration
	}
	args.Duration = req.Duration

	jobResult, err := h.jobManager.Create(job.CreateJobRequest{
		UserID:    ctx.UserID,
		Container: req.Container,
		Host:      req.Hostname,
		JobType:   "tracing",
		Args:      &args,
	})
	if err != nil {
		log.WithError(err).Error("failed to create trace job")
		return response.ErrInternal
	}

	response.Created(ctx, "/v1/traces/"+jobResult.JobID, v1.CreateTraceJobResponse{
		ID: jobResult.JobID,
	})
	return nil
}

// list lists trace jobs with pagination and sorting.
func (h *Handler) list(ctx *server.Context) error {
	listParams, err := ctx.ParseListParams()
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	filter := job.JobQuery{
		Container: ctx.Query("container"),
		Host:      ctx.Query("host"),
		Status:    ctx.Query("status"),
		Type:      "tracing",
	}

	jobs, err := h.jobManager.List(ctx.UserID, ctx.IsAdmin, &filter)
	if err != nil {
		log.WithError(err).Error("failed to list trace jobs")
		return response.ErrInternal
	}

	if err := listing.SortJobs(jobs, listParams.Sort); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	total := len(jobs)
	pageJobs := listing.Paginate(jobs, listParams.Offset, listParams.Limit)

	items := make([]v1.TraceJobResponse, len(pageJobs))
	for i, j := range pageJobs {
		items[i] = convertJobToTraceResponse(j)
	}

	response.Success(ctx, v1.TraceJobListResponse{
		Items:  items,
		Total:  total,
		Limit:  listParams.Limit,
		Offset: listParams.Offset,
	})
	return nil
}

// get gets a specific trace by ID.
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

	response.Success(ctx, convertJobToTraceResponse(jobResult))
	return nil
}

// patchOne stops a single trace job. Body must be {"status":"stopped"}.
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
		log.WithError(err).WithField("job_id", taskID).Error("failed to stop trace job")
		return response.ErrInternal
	}

	response.Success(ctx, nil)
	return nil
}

// patchBulk stops all running trace jobs. Admin only.
// Requires query ?status=running and body {"status":"stopped"}.
func (h *Handler) patchBulk(ctx *server.Context) error {
	if !ctx.IsAdmin {
		return response.ErrForbidden
	}

	if ctx.Query("status") != string(job.JobStatusRunning) {
		return response.ErrInvalidRequest.WithMessage(`bulk patch requires query ?status=running`)
	}

	var req v1.PatchStatusRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}
	if req.Status != listing.StatusStopped {
		return response.ErrInvalidRequest.WithMessage(`status must be "stopped"`)
	}

	h.jobManager.StopAll()
	response.Success(ctx, nil)
	return nil
}

// delete deletes a trace job record by ID.
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
		log.WithError(err).WithField("job_id", taskID).Error("failed to delete trace job")
		return response.ErrInternal
	}

	response.NoContent(ctx)
	return nil
}

// convertJobToTraceResponse maps an internal *job.Job to the v1 wire type.
func convertJobToTraceResponse(jobResult *job.Job) v1.TraceJobResponse {
	return v1.TraceJobResponse{
		ID:          jobResult.JobID,
		AgentTaskID: jobResult.AgentTaskID,
		Container:   jobResult.Container,
		Hostname:    jobResult.Host,
		Status:      string(jobResult.Status),
		StartTime:   jobResult.StartTime.Format("2006-01-02T15:04:05Z07:00"),
		EndTime:     jobResult.EndTime.Format("2006-01-02T15:04:05Z07:00"),
		Results: v1.TraceResults{
			URL: jobResult.Results.URL,
		},
	}
}
