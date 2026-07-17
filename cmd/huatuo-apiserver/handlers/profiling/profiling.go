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
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/listing"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"
	profileService "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/profiling"

	"github.com/gin-gonic/gin/binding"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
)

const (
	ProfilingMemory = "profiling_memory"
	ProfilingCPU    = "profiling_cpu"
	ProfilingLock   = "profiling_lock"
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

// start starts a new profiling job.
func (h *Handler) start(ctx *server.Context) error {
	var req v1.StartProfilingRequest

	if err := ctx.ShouldBindJSON(&req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}
	if req.Duration < 1 {
		return response.ErrInvalidRequest.WithMessage("duration must be at least 1 second")
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
		if err := fillCPUTracerArgs(
			&agentTaskReq,
			req.TargetExecPath,
			req.TargetProcessLanguage,
			profilerToolPath(req.TargetProcessLanguage),
		); err != nil {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
	case "memory":
		agentTaskReq.Interval = config.Get().Profiling.MemoryProfilingInterval
		agentTaskReq.TraceTimeout = config.Get().Profiling.MemorySingleTraceTimeout
		if err := fillMemoryTracerArgs(
			&agentTaskReq,
			req.TargetProcessLanguage,
			req.MemoryMode,
			profilerToolPath(req.TargetProcessLanguage),
		); err != nil {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
	case "lock":
		agentTaskReq.Interval = config.Get().Profiling.CPUProfilingInterval
		agentTaskReq.TraceTimeout = config.Get().Profiling.CPUSingleTraceTimeout
		lockTypes, lockMode, err := fillLockTracerArgs(&agentTaskReq, &req)
		if err != nil {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
		// Persist and report the effective defaults rather than the omitted
		// request values, keeping status responses aligned with tracer args.
		req.LockTypes = lockTypes
		req.LockMode = lockMode
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

	appendProfilingTimingArgs(&agentTaskReq, req.Duration)

	languageValue := req.TargetProcessLanguage
	if req.Type == string(profiling.TypeMemory) && strings.HasPrefix(req.MemoryMode, "NATIVE_") {
		languageValue = string(profiling.LanguageC)
	}
	language, err := profiling.ParseLanguage(languageValue)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}
	implementation, ok := profiling.ImplementationFor(language)
	if !ok {
		return response.ErrInvalidRequest.WithMessage(fmt.Sprintf("unsupported language %q", language))
	}
	if err := validateProfilingTarget(&req, implementation); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	collectionScope := ""
	if implementation == profiling.ImplementationNative {
		collectionScope, err = appendCollectionTracerArgs(&agentTaskReq, &req)
		if err != nil {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
	} else {
		if req.PID > math.MaxInt32 {
			return response.ErrInvalidRequest.WithMessage(fmt.Sprintf("pid %d exceeds Linux PID range", req.PID))
		}
		if req.PID != 0 {
			agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--pid", strconv.FormatUint(req.PID, 10))
		}
		if err := appendProfileLabelArgs(&agentTaskReq, req.Labels); err != nil {
			return response.ErrInvalidRequest.WithMessage(err.Error())
		}
	}

	if config.Get().Profiling.MaxProfilerProcesses > 0 {
		agentTaskReq.TracerArgs = append(
			agentTaskReq.TracerArgs,
			"--max-concurrent-procs",
			strconv.Itoa(config.Get().Profiling.MaxProfilerProcesses),
		)
	}

	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs,
		"--output-format", "remote",
		"--output-storage", "/var/run/huatuo-toolstream.sock")

	var jobType string
	if req.Type == "memory" {
		jobType = ProfilingMemory
	} else if req.Type == "lock" {
		jobType = ProfilingLock
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
		"scope":                   collectionScope,
		// Store integer identifiers as decimal strings. PrivateData is decoded
		// through map[string]any, where JSON numbers otherwise become float64
		// and can lose precision (notably for 64-bit cgroup IDs).
		"pid":              strconv.FormatUint(req.PID, 10),
		"cgroup_id":        strconv.FormatUint(req.CgroupID, 10),
		"cgroup_path":      req.CgroupPath,
		"process_group_id": strconv.Itoa(req.ProcessGroupID),
		"lock_types":       req.LockTypes,
		"lock_mode":        req.LockMode,
		"lock_min_wait":    req.LockMinWait,
		"labels":           req.Labels,
	}

	response.Created(ctx, "/v1/profiles/"+jobResult.JobID, v1.StartProfilingResponse{
		ID: jobResult.JobID,
	})
	return nil
}

func appendProfilingTimingArgs(agentTaskReq *job.NewAgentTaskReq, requestedDuration int) {
	// The job manager controls the overall lifetime; each profiler invocation
	// emits exactly one interval-sized snapshot.
	agentTaskReq.Duration = requestedDuration * 2
	interval := strconv.Itoa(agentTaskReq.Interval)
	agentTaskReq.TracerArgs = append(
		agentTaskReq.TracerArgs,
		"--duration", interval,
		"--aggr-interval", interval,
	)
}

func profilerToolPath(language string) string {
	switch language {
	case string(profiling.LanguageJava):
		return config.Get().Profiling.JavaProfilerToolPath
	case string(profiling.LanguagePython):
		return config.Get().Profiling.PythonProfilerToolPath
	default:
		return ""
	}
}

func validateProfilingTarget(req *v1.StartProfilingRequest, implementation profiling.Implementation) error {
	if implementation != profiling.ImplementationNative {
		if req.Scope != "" || req.CgroupID != 0 || req.CgroupPath != "" || req.ProcessGroupID != 0 {
			return fmt.Errorf("collection dimensions are only supported for native profiling")
		}
		if (req.PID != 0) == (req.Container != "") {
			return fmt.Errorf("exactly one of pid or container must be provided")
		}
		return nil
	}

	if req.Type != string(profiling.TypeMemory) {
		return nil
	}
	hasPID := req.PID != 0
	hasContainerOrCgroup := req.Container != "" || req.CgroupID != 0 || req.CgroupPath != ""
	hasProcessGroup := req.ProcessGroupID != 0
	targets := 0
	for _, present := range []bool{hasPID, hasContainerOrCgroup, hasProcessGroup} {
		if present {
			targets++
		}
	}
	if targets != 1 {
		return fmt.Errorf("exactly one PID/TGID, container/cgroup, or process group target must be provided")
	}
	return nil
}

func fillLockTracerArgs(agentTaskReq *job.NewAgentTaskReq, req *v1.StartProfilingRequest) ([]string, string, error) {
	if req.TargetExecPath != "" {
		return nil, "", fmt.Errorf("target_exec_path is not supported for native profiling")
	}
	language, err := profiling.ParseLanguage(req.TargetProcessLanguage)
	if err != nil || !profiling.IsSupported(language, profiling.TypeLock) {
		return nil, "", fmt.Errorf("kernel lock profiling requires target_process_language c, c++, or go")
	}
	lockTypes := req.LockTypes
	if len(lockTypes) == 0 {
		lockTypes = []string{"mutex", "spinlock", "rwlock"}
	}
	valid := map[string]bool{"mutex": true, "spinlock": true, "rwlock": true}
	normalizedTypes := make([]string, 0, len(lockTypes))
	seen := make(map[string]bool, len(lockTypes))
	for _, lockType := range lockTypes {
		lockType = strings.ToLower(strings.TrimSpace(lockType))
		if !valid[lockType] {
			return nil, "", fmt.Errorf("unsupported kernel lock type %q", lockType)
		}
		if !seen[lockType] {
			seen[lockType] = true
			normalizedTypes = append(normalizedTypes, lockType)
		}
	}
	if len(normalizedTypes) == 0 {
		return nil, "", fmt.Errorf("at least one kernel lock type is required")
	}
	mode := strings.ToLower(strings.TrimSpace(req.LockMode))
	if mode == "" {
		mode = "time"
	}
	if mode != "time" && mode != "count" {
		return nil, "", fmt.Errorf("unsupported lock mode %q", mode)
	}

	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs,
		"-t", "lock",
		"-l", string(language),
		"--lock-types", strings.Join(normalizedTypes, ","),
		"--lock-mode", mode,
	)
	if req.LockMinWait != "" {
		minWait, err := time.ParseDuration(req.LockMinWait)
		if err != nil {
			return nil, "", fmt.Errorf("invalid lock_min_wait %q: %w", req.LockMinWait, err)
		}
		if minWait < 0 || minWait > time.Hour {
			return nil, "", fmt.Errorf("lock_min_wait must be between 0 and 1h")
		}
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--lock-min-wait", req.LockMinWait)
	}
	return normalizedTypes, mode, nil
}

func appendCollectionTracerArgs(agentTaskReq *job.NewAgentTaskReq, req *v1.StartProfilingRequest) (string, error) {
	scope := req.Scope
	if scope == "" {
		switch {
		case req.CgroupID != 0 || req.CgroupPath != "" || req.Container != "":
			scope = "cgroup"
		case req.ProcessGroupID != 0:
			scope = "process-group"
		case req.PID != 0:
			scope = "tgid"
		default:
			scope = "all"
		}
	}
	validScopes := map[string]bool{
		"all": true, "pid": true, "thread": true, "tgid": true,
		"thread-group": true, "cgroup": true, "process-group": true,
	}
	if !validScopes[scope] {
		return "", fmt.Errorf("unsupported profiling scope %q", scope)
	}
	// Canonical scope names are used in status responses, tracer arguments,
	// pprof labels, and backend selectors. Keep accepting the original CLI
	// vocabulary without leaking two names for the same dimension.
	switch scope {
	case "thread":
		scope = "pid"
	case "thread-group":
		scope = "tgid"
	}
	if req.PID > math.MaxInt32 {
		return "", fmt.Errorf("pid %d exceeds Linux PID range", req.PID)
	}
	if req.CgroupID != 0 && req.CgroupPath != "" {
		return "", fmt.Errorf("only one of cgroup_id or cgroup_path may be specified")
	}
	if req.ProcessGroupID < 0 || uint64(req.ProcessGroupID) > math.MaxInt32 {
		return "", fmt.Errorf("process_group_id must be between 0 and %d", math.MaxInt32)
	}
	switch scope {
	case "pid", "tgid":
		if req.PID == 0 {
			return "", fmt.Errorf("scope %s requires pid", scope)
		}
	case "cgroup":
		if req.CgroupID == 0 && req.CgroupPath == "" && req.Container == "" {
			return "", fmt.Errorf("scope cgroup requires cgroup_id, cgroup_path, or container")
		}
	case "process-group":
		if req.ProcessGroupID == 0 && req.PID == 0 {
			return "", fmt.Errorf("scope process-group requires process_group_id or pid")
		}
	}
	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--scope", scope)
	if req.PID != 0 {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--pid", strconv.FormatUint(req.PID, 10))
	}
	if req.CgroupID != 0 {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--cgroup-id", strconv.FormatUint(req.CgroupID, 10))
	}
	if req.CgroupPath != "" {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--cgroup-path", req.CgroupPath)
	}
	if req.ProcessGroupID != 0 {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--process-group-id", strconv.Itoa(req.ProcessGroupID))
	}

	if err := appendProfileLabelArgs(agentTaskReq, req.Labels); err != nil {
		return "", err
	}
	return scope, nil
}

func appendProfileLabelArgs(agentTaskReq *job.NewAgentTaskReq, labels map[string]string) error {
	labelNames := make([]string, 0, len(labels))
	for name := range labels {
		if err := profiler.ValidateCustomLabelName(name); err != nil {
			return err
		}
		labelNames = append(labelNames, name)
	}
	sort.Strings(labelNames)
	for _, name := range labelNames {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--label", name+"="+labels[name])
	}
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

func fillMemoryTracerArgs(agentTaskReq *job.NewAgentTaskReq, targetProcessLanguage, memoryMode, toolPath string) error {
	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "-t", string(profiling.TypeMemory))

	languageValue := targetProcessLanguage
	modeValue := strings.ToLower(memoryMode)
	if strings.HasPrefix(memoryMode, "NATIVE_") {
		languageValue = string(profiling.LanguageC)
		modeValue = strings.ToLower(strings.TrimPrefix(memoryMode, "NATIVE_"))
	}
	language, err := profiling.ParseLanguage(languageValue)
	if err != nil {
		return fmt.Errorf("memory profiling not supported for %s", targetProcessLanguage)
	}
	mode, err := profiling.ParseMemoryMode(modeValue)
	if err != nil || !profiling.SupportsMemoryMode(language, mode) {
		return fmt.Errorf("memory mode not supported: %s", memoryMode)
	}

	agentTaskReq.TracerArgs = append(
		agentTaskReq.TracerArgs,
		"--memory-mode", string(mode),
		"-l", string(language),
	)
	implementation, _ := profiling.ImplementationFor(language)
	if implementation != profiling.ImplementationNative {
		if toolPath == "" {
			return fmt.Errorf("%s profiling requires a configured tool path", language)
		}
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--tool-path", toolPath)
	}
	return nil
}

func fillCPUTracerArgs(agentTaskReq *job.NewAgentTaskReq, targetExecPath, targetProcessLanguage, toolPath string) error {
	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "-t", string(profiling.TypeCPU))

	language, err := profiling.ParseLanguage(targetProcessLanguage)
	if err != nil || !profiling.IsSupported(language, profiling.TypeCPU) {
		return fmt.Errorf("cpu profiling not supported for %s", targetProcessLanguage)
	}
	implementation, _ := profiling.ImplementationFor(language)
	if implementation == profiling.ImplementationNative && targetExecPath != "" {
		return fmt.Errorf("target_exec_path is not supported for native profiling")
	}
	if targetExecPath != "" {
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--binary-match-path", targetExecPath)
	}
	agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "-l", string(language))
	if implementation != profiling.ImplementationNative {
		if toolPath == "" {
			return fmt.Errorf("%s profiling requires a configured tool path", language)
		}
		agentTaskReq.TracerArgs = append(agentTaskReq.TracerArgs, "--tool-path", toolPath)
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
		"lock":   true,
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
	if jobType == "lock" || jobType == "" {
		typesToQuery = append(typesToQuery, ProfilingLock)
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
	case ProfilingLock:
		resp.Type = "lock"
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
		if value, ok := jobResult.PrivateData["scope"].(string); ok {
			resp.Scope = value
		}
		if value, ok := privateDataUint64(jobResult.PrivateData["pid"]); ok {
			resp.PID = value
		}
		if value, ok := privateDataUint64(jobResult.PrivateData["cgroup_id"]); ok {
			resp.CgroupID = value
		}
		if value, ok := jobResult.PrivateData["cgroup_path"].(string); ok {
			resp.CgroupPath = value
		}
		if value, ok := privateDataInt(jobResult.PrivateData["process_group_id"]); ok {
			resp.ProcessGroupID = value
		}
		if value, ok := privateDataStringSlice(jobResult.PrivateData["lock_types"]); ok {
			resp.LockTypes = append([]string(nil), value...)
		}
		if value, ok := jobResult.PrivateData["lock_mode"].(string); ok {
			resp.LockMode = value
		}
		if value, ok := jobResult.PrivateData["lock_min_wait"].(string); ok {
			resp.LockMinWait = value
		}
		if value, ok := privateDataStringMap(jobResult.PrivateData["labels"]); ok {
			resp.Labels = value
		}
	}

	return resp
}

// PrivateData is persisted as untyped JSON. Accept both the lossless decimal
// string representation used by new jobs and numeric values produced by older
// in-memory/JSON-backed jobs.
func privateDataUint64(value any) (uint64, bool) {
	switch typed := value.(type) {
	case string:
		parsed, err := strconv.ParseUint(typed, 10, 64)
		return parsed, err == nil
	case json.Number:
		parsed, err := strconv.ParseUint(string(typed), 10, 64)
		return parsed, err == nil
	case uint64:
		return typed, true
	case uint32:
		return uint64(typed), true
	case uint:
		return uint64(typed), true
	case int:
		if typed >= 0 {
			return uint64(typed), true
		}
	case int64:
		if typed >= 0 {
			return uint64(typed), true
		}
	case float64:
		// float64(math.MaxUint64) rounds up to 2^64, so use a strict
		// comparison to avoid accepting that out-of-range boundary.
		if typed >= 0 && typed < float64(math.MaxUint64) && math.Trunc(typed) == typed {
			return uint64(typed), true
		}
	}
	return 0, false
}

func privateDataInt(value any) (int, bool) {
	switch typed := value.(type) {
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 32)
		return int(parsed), err == nil
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 32)
		return int(parsed), err == nil
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		if typed >= math.MinInt32 && typed <= math.MaxInt32 {
			return int(typed), true
		}
	case float64:
		if typed >= math.MinInt32 && typed <= math.MaxInt32 && math.Trunc(typed) == typed {
			return int(typed), true
		}
	}
	return 0, false
}

func privateDataStringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		values := make([]string, len(typed))
		for i, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			values[i] = text
		}
		return values, true
	default:
		return nil, false
	}
}

func privateDataStringMap(value any) (map[string]string, bool) {
	switch typed := value.(type) {
	case map[string]string:
		return typed, true
	case map[string]any:
		values := make(map[string]string, len(typed))
		for name, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			values[name] = text
		}
		return values, true
	default:
		return nil, false
	}
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
		log.Warnf("SelectMergeStacktraces failed: %v", err)
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
