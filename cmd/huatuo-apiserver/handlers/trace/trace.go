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
	"context"
	"errors"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
)

// MaxTraceTimeout is the maximum allowed trace duration in seconds.
const MaxTraceTimeout = 300

const traceJobType = job.JobTypeTracing

// Handler handles trace-related HTTP requests.
type Handler struct {
	jobManager jobManager
	Handlers   []server.Handle
}

type jobManager interface {
	CreateContext(ctx context.Context, request *job.CreateJobRequest) (*job.Job, error)
	ListPageContext(ctx context.Context, userID string, isAdmin bool, query *job.JobQuery) (*job.JobPage, error)
	GetByTypesContext(ctx context.Context, jobID string, expectedTypes ...job.JobType) (*job.Job, error)
	StopByTypesContext(ctx context.Context, jobID string, force bool, expectedTypes ...job.JobType) error
	StopAllByTypesContext(ctx context.Context, expectedTypes ...job.JobType) error
	DeleteByTypesContext(ctx context.Context, jobID string, expectedTypes ...job.JobType) error
}

// NewHandler creates a new trace handler.
func NewHandler(jm jobManager) *Handler {
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
		return response.ErrInvalidRequest
	}
	if err := validateCreateTraceJobRequest(&req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	args := job.AgentTaskRequest{
		TracerName:  "tracer",
		DataType:    "db",
		ContainerID: req.ContainerID,
	}

	if req.Type != "tracing" {
		args.TracerName = req.Type
	}

	args.TraceTimeout = req.Duration
	args.Duration = req.Duration

	jobResult, err := h.jobManager.CreateContext(ctx.Request().Context(), &job.CreateJobRequest{
		UserID:      ctx.UserID,
		ContainerID: req.ContainerID,
		Hostname:    req.Hostname,
		Type:        traceJobType,
		AgentTask:   &args,
	})
	if err != nil {
		if errors.Is(err, job.ErrQuotaExceeded) {
			return response.ErrConflict.WithMessage("trace job quota exceeded")
		}
		log.WithError(err).Error("failed to create trace job")
		return response.ErrInternal
	}

	response.Created(ctx, "/v1/traces/"+jobResult.ID, v1.CreateTraceJobResponse{
		ID: jobResult.ID,
	})
	return nil
}

func validateCreateTraceJobRequest(req *v1.CreateTraceJobRequest) error {
	if req.Hostname == "" {
		return errors.New("hostname is required")
	}
	if req.Duration <= 0 || req.Duration > MaxTraceTimeout {
		return errors.New("duration must be between 1 and 300 seconds")
	}
	if req.Type == "" {
		return errors.New("type is required")
	}
	return nil
}

// list lists trace jobs with pagination and sorting.
func (h *Handler) list(ctx *server.Context) error {
	listParams, err := ctx.ParseListParams()
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	filter := job.JobQuery{
		ContainerID: firstQuery(ctx, "container_id", "container"),
		Hostname:    firstQuery(ctx, "hostname", "host"),
		Status:      ctx.Query("status"),
		Types:       []job.JobType{traceJobType},
		Sort:        listParams.Sort,
		Limit:       listParams.Limit,
		Offset:      listParams.Offset,
	}

	page, err := h.jobManager.ListPageContext(ctx.Request().Context(), ctx.UserID, ctx.IsAdmin, &filter)
	if err != nil {
		if errors.Is(err, job.ErrInvalidQuery) {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
		log.WithError(err).Error("failed to list trace jobs")
		return response.ErrInternal
	}

	items := make([]v1.TraceJobResponse, len(page.Items))
	for i, j := range page.Items {
		items[i] = convertJobToTraceResponse(j)
	}

	response.Success(ctx, v1.TraceJobListResponse{
		Items:  items,
		Total:  int(page.Total),
		Limit:  listParams.Limit,
		Offset: listParams.Offset,
	})
	return nil
}

func firstQuery(ctx *server.Context, preferred, legacy string) string {
	if value := ctx.Query(preferred); value != "" {
		return value
	}
	return ctx.Query(legacy)
}

// get gets a specific trace by ID.
func (h *Handler) get(ctx *server.Context) error {
	taskID := ctx.Param("id")
	if taskID == "" {
		return response.ErrInvalidRequest.WithMessage("id is required")
	}

	jobResult, err := h.jobManager.GetByTypesContext(ctx.Request().Context(), taskID, traceJobType)
	if err != nil {
		if errors.Is(err, job.ErrNotFound) {
			return response.ErrNotFound
		}
		log.WithError(err).WithField("job_id", taskID).Error("failed to get trace job")
		return response.ErrInternal
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
	if req.Status != string(job.JobStatusStopped) {
		return response.ErrInvalidRequest.WithMessage(`status must be "stopped"`)
	}

	jobResult, err := h.jobManager.GetByTypesContext(ctx.Request().Context(), taskID, traceJobType)
	if err != nil {
		if errors.Is(err, job.ErrNotFound) {
			return response.ErrNotFound
		}
		log.WithError(err).WithField("job_id", taskID).Error("failed to get trace job")
		return response.ErrInternal
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}

	if jobResult.Status != job.JobStatusPending && jobResult.Status != job.JobStatusRunning {
		return response.ErrInvalidRequest.WithMessage("job already completed")
	}

	if err := h.jobManager.StopByTypesContext(ctx.Request().Context(), taskID, false, traceJobType); err != nil {
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
	if req.Status != string(job.JobStatusStopped) {
		return response.ErrInvalidRequest.WithMessage(`status must be "stopped"`)
	}

	if err := h.jobManager.StopAllByTypesContext(ctx.Request().Context(), traceJobType); err != nil {
		log.WithError(err).Error("failed to stop all trace jobs")
		return response.ErrInternal
	}
	response.Success(ctx, nil)
	return nil
}

// delete deletes a trace job record by ID.
func (h *Handler) delete(ctx *server.Context) error {
	taskID := ctx.Param("id")
	if taskID == "" {
		return response.ErrInvalidRequest.WithMessage("id is required")
	}

	jobResult, err := h.jobManager.GetByTypesContext(ctx.Request().Context(), taskID, traceJobType)
	if err != nil {
		if errors.Is(err, job.ErrNotFound) {
			return response.ErrNotFound
		}
		log.WithError(err).WithField("job_id", taskID).Error("failed to get trace job")
		return response.ErrInternal
	}

	if !ctx.CanAccessTask(jobResult.UserID) {
		return response.ErrForbidden
	}

	if err := h.jobManager.DeleteByTypesContext(ctx.Request().Context(), taskID, traceJobType); err != nil {
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
		ID:           jobResult.ID,
		AgentTaskID:  jobResult.AgentTaskID,
		ContainerID:  jobResult.ContainerID,
		Hostname:     jobResult.Hostname,
		Status:       string(jobResult.Status),
		StartTime:    formatTime(jobResult.StartTime),
		EndTime:      formatTime(jobResult.EndTime),
		ErrorMessage: jobResult.ErrorMessage,
		Results: v1.TraceResults{
			URL: jobResult.Result.URL,
		},
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339Nano)
}
