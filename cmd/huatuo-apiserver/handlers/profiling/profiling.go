// Copyright 2025 The HuaTuo Authors
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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/listing"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/log"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"

	"github.com/gin-gonic/gin/binding"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
)

const (
	ProfilingMemory = "profiling_memory"
	ProfilingCPU    = "profiling_cpu"
)

var (
	supportedLanguages = map[string]bool{
		"c++":  true,
		"c":    true,
		"go":   true,
		"java": true,
	}
	supportedMemoryModes = map[string]string{
		"NATIVE_PHYSICAL_ALLOC": "native_physical_alloc",
		"NATIVE_PHYSICAL_USAGE": "native_physical_usage",
		"NATIVE_VIRTUAL_ALLOC":  "native_virtual_alloc",
		"OBJECT_ALLOC":          "object_alloc",
		"OBJECT_USAGE":          "object_usage",
	}
)

// Handler handles profiling-related HTTP requests.
type Handler struct {
	jobManager *job.Manager
	Handlers   []server.Handle
}

// NewHandler creates a new profiling handler.
func NewHandler(jm *job.Manager) *Handler {
	h := &Handler{jobManager: jm}

	h.Handlers = []server.Handle{
		{Typ: server.HttpGet, Uri: "/capabilities", Handle: h.capabilities},
		{Typ: server.HttpPost, Uri: "", Handle: h.start},
		{Typ: server.HttpGet, Uri: "", Handle: h.list},
		{Typ: server.HttpGet, Uri: "/:id", Handle: h.get},
		{Typ: server.HttpGet, Uri: "/:id/raw", Handle: h.getRawData},
		{Typ: server.HttpPatch, Uri: "/:id", Handle: h.patchOne},
		{Typ: server.HttpDelete, Uri: "/:id", Handle: h.delete},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/SelectMergeStacktraces", Handle: h.DisplaySelectMergeStacktraces},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/ProfileTypes", Handle: h.DisplayProfileTypes},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/SelectSeries", Handle: h.DisplaySelectSeries},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/LabelNames", Handle: h.DisplayLabelNames},
		{Typ: server.HttpPost, Uri: "/flamegraph/querier.v1.QuerierService/LabelValues", Handle: h.DisplayLabelValues},
	}

	return h
}

func isLanguageSupported(lang string) bool {
	return supportedLanguages[lang]
}

func isMemoryModeSupported(mode string) (string, bool) {
	if memoryMode, ok := supportedMemoryModes[mode]; ok {
		return memoryMode, true
	}
	return "", false
}

// start starts a new profiling job.
func (h *Handler) start(ctx *server.Context) error {
	var req v1.StartProfilingRequest

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

	agentTaskReq := job.NewAgentTaskReq{
		TracerName: "profiler",
		DataType:   "db-json",
	}
	switch req.Type {
	case "cpu":
		agentTaskReq.Interval = config.Get().Profiling.CPUProfilingInterval
		agentTaskReq.TraceTimeout = config.Get().Profiling.CPUSingleTraceTimeout
		if err := fillCPUTracerArgs(&agentTaskReq, req.TargetExecPath, req.TargetProcessLanguage); err != nil {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
	case "memory":
		agentTaskReq.Interval = config.Get().Profiling.MemoryProfilingInterval
		agentTaskReq.TraceTimeout = config.Get().Profiling.MemorySingleTraceTimeout
		if err := fillMemoryTracerArgs(&agentTaskReq, req.TargetProcessLanguage, req.MemoryMode); err != nil {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
	default:
		return response.ErrInvalidRequest.WithMessage("not supported yet")
	}

	if agentTaskReq.Interval == 0 {
		log.Infof("CPUProfilingInterval or MemoryProfilingInterval is not set, using default value 10")
		agentTaskReq.Interval = 10
	}
	if agentTaskReq.TraceTimeout < agentTaskReq.Interval*2 {
		log.Infof("CPUSingleTraceTimeout or MemorySingleTraceTimeout is less than Interval * 2, using Interval * 2")
		agentTaskReq.TraceTimeout = agentTaskReq.Interval * 2
	}

	// profiling job need to be stopped from outside, so we need to set duration to args.Duration * 2,
	// job.Duration will control the actual profiling time
	agentTaskReq.Duration = req.Duration * 2
	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--duration", strconv.Itoa(agentTaskReq.Interval))

	if config.Get().Profiling.ThirdPartyToolLimit > 0 {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--tool-limit", strconv.Itoa(config.Get().Profiling.ThirdPartyToolLimit))
	}

	var jobType string
	if req.Type == "memory" {
		jobType = ProfilingMemory
	} else {
		jobType = ProfilingCPU
	}
	jobResult, err := h.jobManager.Create(job.CreateJobRequest{
		UserID:    ctx.UserID,
		Container: req.Container,
		Host:      req.Hostname,
		JobType:   jobType,
		Args:      &agentTaskReq,
	})
	if err != nil {
		log.Errorf("Failed to create profiling job: %v", err)
		return response.ErrInternal.WithMessage(err.Error())
	}
	jobResult.PrivateData = map[string]any{
		"target_exec_path":        req.TargetExecPath,
		"target_process_language": req.TargetProcessLanguage,
		"memory_mode":             req.MemoryMode,
	}

	response.Created(ctx, "/v1/profiles/"+jobResult.JobID, v1.StartProfilingResponse{
		ID: jobResult.JobID,
	})
	return nil
}

// hasRunningProfilingJob reports whether a profiling job is currently running on hostname for userID.
func (h *Handler) hasRunningProfilingJob(hostname, userID string) (bool, error) {
	filter := job.JobQuery{
		Host:   hostname,
		Status: "running",
	}
	jobs, err := h.jobManager.List(userID, false, &filter)
	if err != nil {
		log.Errorf("Failed to list profiling jobs: %v", err)
		return false, err
	}
	if len(jobs) > 0 {
		log.Infof("There is already a profiling job running on this host")
		return true, nil
	}
	return false, nil
}

func fillMemoryTracerArgs(agentTaskReq *job.NewAgentTaskReq, targetProcessLanguage, memoryMode string) error {
	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "-t", "mem")

	if mode, ok := isMemoryModeSupported(memoryMode); ok {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--flags", fmt.Sprintf("--mode %s", mode))
	} else {
		return fmt.Errorf("memory mode not supported: %s", memoryMode)
	}

	// memory native mode do not need to pass language in frontend, just set it to c
	if memoryMode == "NATIVE_PHYSICAL_ALLOC" || memoryMode == "NATIVE_PHYSICAL_USAGE" || memoryMode == "NATIVE_VIRTUAL_ALLOC" {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "-l", "c")
		return nil
	}

	if isLanguageSupported(targetProcessLanguage) {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "-l", targetProcessLanguage)
	} else {
		return fmt.Errorf("memory profiling not supported for %s", targetProcessLanguage)
	}

	return nil
}

func fillCPUTracerArgs(agentTaskReq *job.NewAgentTaskReq, targetExecPath, targetProcessLanguage string) error {
	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "-t", "cpu")

	if targetExecPath != "" {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--exec-path", targetExecPath)
	}

	if isLanguageSupported(targetProcessLanguage) {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "-l", targetProcessLanguage)
	} else if targetProcessLanguage == "python" {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "-l", "python")
	} else {
		return fmt.Errorf("cpu profiling not supported for %s", targetProcessLanguage)
	}

	return nil
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
		log.Errorf("Failed to stop profiling job: %v", err)
		return response.ErrInternal.WithMessage(err.Error())
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

	jobType := ctx.Query("type")
	validTypes := map[string]bool{
		"memory": true,
		"cpu":    true,
		"":       true,
	}
	if !validTypes[jobType] {
		return response.ErrInvalidRequest.WithMessage("invalid type value")
	}

	filter := job.JobQuery{
		Container: ctx.Query("container"),
		Host:      ctx.Query("host"),
		Status:    ctx.Query("status"),
	}
	var allJobs []*job.Job
	var listErr error
	typesToQuery := []string{}
	if jobType == "memory" || jobType == "" {
		typesToQuery = append(typesToQuery, ProfilingMemory)
	}
	if jobType == "cpu" || jobType == "" {
		typesToQuery = append(typesToQuery, ProfilingCPU)
	}
	for _, queryType := range typesToQuery {
		currentFilter := filter
		currentFilter.Type = queryType

		jobs, err := h.jobManager.List(ctx.UserID, ctx.IsAdmin, &currentFilter)
		if err != nil {
			log.Errorf("Failed to list %s jobs: %v", queryType, err)
			listErr = err
			continue
		}
		allJobs = append(allJobs, jobs...)
	}
	if listErr != nil && len(allJobs) == 0 {
		return response.ErrInternal.WithMessage(listErr.Error())
	}

	if err := listing.SortJobs(allJobs, listParams.Sort); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	total := len(allJobs)
	pageJobs := listing.Paginate(allJobs, listParams.Offset, listParams.Limit)

	items := make([]v1.ProfilingStatusResponse, len(pageJobs))
	for i, j := range pageJobs {
		items[i] = h.convertJobToProfilingResponse(j)
	}

	response.Success(ctx, v1.ProfilingListResponse{
		Items:  items,
		Total:  total,
		Limit:  listParams.Limit,
		Offset: listParams.Offset,
	})
	return nil
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

	profilingResponse := h.convertJobToProfilingResponse(jobResult)

	response.Success(ctx, profilingResponse)
	return nil
}

// convertJobToProfilingResponse converts a job.Job to v1.ProfilingStatusResponse.
func (h *Handler) convertJobToProfilingResponse(jobResult *job.Job) v1.ProfilingStatusResponse {
	if jobResult.Status == job.JobStatusCompleted || jobResult.Status == job.JobStatusStopped {
		if jobResult.Results.URL == "" {
			jobResult.Results.URL = getFlameGraphURL(jobResult)
			if err := h.jobManager.Save(jobResult); err != nil {
				log.Errorf("Failed to save job %s: %v", jobResult.JobID, err)
			}
		}
	}

	resp := v1.ProfilingStatusResponse{
		ID:          jobResult.JobID,
		AgentTaskID: jobResult.AgentTaskID,
		Container:   jobResult.Container,
		Hostname:    jobResult.Host,
		Status:      string(jobResult.Status),
		StartTime:   jobResult.StartTime.Format("2006-01-02T15:04:05.000"),
		EndTime:     jobResult.EndTime.Format("2006-01-02T15:04:05.000"),
		TracerArgs:  jobResult.Args.TracerArgs,
		Duration:    jobResult.Args.Duration >> 1,
		Results: v1.ProfilingResults{
			URL: jobResult.Results.URL,
		},
		ErrorMessage: jobResult.Error,
	}

	switch jobResult.Type {
	case ProfilingMemory:
		resp.Type = "memory"
	case ProfilingCPU:
		resp.Type = "cpu"
	}

	if jobResult.PrivateData != nil {
		if memoryMode, ok := jobResult.PrivateData["memory_mode"]; ok && memoryMode != nil {
			if memoryModeStr, ok := memoryMode.(string); ok {
				resp.MemoryMode = memoryModeStr
			}
		}
		if targetExecPath, ok := jobResult.PrivateData["target_exec_path"]; ok && targetExecPath != nil {
			if targetExecPathStr, ok := targetExecPath.(string); ok {
				resp.TargetExecPath = targetExecPathStr
			}
		}
		if targetProcessLanguage, ok := jobResult.PrivateData["target_process_language"]; ok && targetProcessLanguage != nil {
			if targetProcessLanguageStr, ok := targetProcessLanguage.(string); ok {
				resp.TargetProcessLanguage = targetProcessLanguageStr
			}
		}
	}

	return resp
}

func getFlameGraphURL(jobResult *job.Job) string {
	base := config.Get().Profiling.FlameGraphBaseURL

	var dashboardUid string
	var dashboardSlug string
	var labelKey string
	var labelVal string

	from := jobResult.StartTime.UTC().Format("2006-01-02T15:04:05.000Z")
	to := jobResult.EndTime.UTC().Format("2006-01-02T15:04:05.000Z")

	if jobResult.Container != "" {
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
		labelVal = jobResult.Container
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
		labelVal = jobResult.Host
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
		log.Errorf("Failed to delete profiling job: %v", err)
		return response.ErrInternal.WithMessage(err.Error())
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
		log.Errorf("Failed to get raw profiling data: %v", err)
		return response.ErrInternal.WithMessage(err.Error())
	}

	response.Success(ctx, v1.RawDataResponse{
		Data: profiles,
	})
	return nil
}

// DisplaySelectMergeStacktraces handles /querier.v1.QuerierService/SelectMergeStacktraces.
func (h *Handler) DisplaySelectMergeStacktraces(ctx *server.Context) error {
	req := &querierv1.SelectMergeStacktracesRequest{}
	if err := ctx.ShouldBindBodyWith(req, binding.ProtoBuf); err != nil {
		ctx.JSON(http.StatusBadRequest, map[string]any{"message": err.Error()})
		return nil
	}

	log.Infof("DisplaySelectMergeStacktraces request: %v", req)

	resp, err := profileService.SelectMergeStacktraces(req)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": err.Error()})
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

	log.Infof("DisplayProfileTypes request: %v", req)

	resp, err := profileService.ProfileTypes(req)
	if err != nil {
		log.Errorf("Failed to get profile types: %v", err)
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": err.Error()})
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

	log.Infof("DisplaySelectSeries request: %v", req)

	resp, err := profileService.SelectSeries(req)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": err.Error()})
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

	log.Infof("DisplayLabelNames request: %v", req)

	resp, err := profileService.LabelNames(req)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": err.Error()})
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

	log.Infof("DisplayLabelValues request: %v", req)

	resp, err := profileService.LabelValues(req)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return nil
	}

	// fix internal: invalid content-type: "application/x-protobuf"; expecting "application/proto"
	ctx.Header("Content-Type", "application/proto")
	ctx.ProtoBuf(http.StatusOK, resp)
	return nil
}