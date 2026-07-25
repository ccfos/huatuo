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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/pkg/profiling"
)

type profilingJobListQuery struct {
	ContainerID string `form:"container_id"`
	Hostname    string `form:"hostname"`
	Status      string `form:"status"`
	Type        string `form:"type"`
}

const defaultLockWaitThreshold = "1us"

type profilingJobListRequest struct {
	ListParams server.ListParams
	JobQuery   job.JobQuery
}

type patchProfilingJobRequest struct {
	ID     string
	Status string
}

type profilingJobPrivateData struct {
	BinaryMatchPath   string             `json:"binary_match_path"`
	ToolPath          string             `json:"tool_path"`
	Duration          int                `json:"duration"`
	Language          string             `json:"language"`
	MemoryMode        string             `json:"memory_mode"`
	CPUIDs            []int              `json:"cpu_ids,omitempty"`
	PID               int                `json:"pid,omitempty"`
	ThreadGroup       bool               `json:"thread_group,omitempty"`
	LockMode          profiling.LockMode `json:"lock_mode,omitempty"`
	LockType          profiling.LockType `json:"lock_type,omitempty"`
	LockWaitThreshold string             `json:"lock_wait_threshold,omitempty"`
}

func parseCreateProfilingJobRequest(ctx *server.Context) (*v1.CreateProfilingJobRequest, error) {
	req := &v1.CreateProfilingJobRequest{}
	if err := ctx.ShouldBindJSON(req); err != nil {
		return nil, err
	}
	return req, nil
}

func buildCreateProfilingJobRequest(
	req *v1.CreateProfilingJobRequest,
	userID string,
	cfg *Config,
) (*job.CreateJobRequest, error) {
	taskReq := job.AgentTaskRequest{
		TracerName:   "profiler",
		DataType:     "db-json",
		ContainerID:  req.ContainerID,
		Interval:     cfg.AggregationInterval,
		TraceTimeout: cfg.ExecutionTimeout,
	}

	var targetArgs []string
	var jobType job.JobType
	var err error
	if req.ProfilingType == string(profiling.TypeLock) {
		jobType, err = buildProfilingTracerArgs(&taskReq, req)
		if err == nil {
			targetArgs, err = profilingTargetArgs(req)
		}
	} else {
		targetArgs, err = profilingTargetArgs(req)
		if err == nil {
			jobType, err = buildProfilingTracerArgs(&taskReq, req)
		}
	}
	if err != nil {
		return nil, err
	}
	taskReq.TracerArgs = append(taskReq.TracerArgs, targetArgs...)
	if req.Duration < taskReq.Interval*2 {
		return nil, errors.New("duration must cover at least two profiling intervals")
	}
	if req.Duration+taskReq.Interval >= 3600 {
		return nil, errors.New("duration plus profiling interval must be less than 3600 seconds")
	}
	if taskReq.TraceTimeout < req.Duration+taskReq.Interval {
		taskReq.TraceTimeout = req.Duration + taskReq.Interval
	}

	// The job duration controls profiling lifetime while the agent task remains
	// alive long enough to be stopped externally.
	taskReq.Duration = req.Duration * 2
	taskReq.TracerArgs = append(
		taskReq.TracerArgs,
		"--duration", strconv.Itoa(req.Duration),
		"--aggr-interval", strconv.Itoa(taskReq.Interval),
		"--max-concurrent-procs", strconv.Itoa(cfg.MaxProfilerProcs),
		"--output-format", "remote",
		"--output-storage", "/var/run/huatuo-toolstream.sock",
	)

	privateData, err := newProfilingPrivateData(req)
	if err != nil {
		return nil, err
	}

	return &job.CreateJobRequest{
		UserID:      userID,
		ContainerID: req.ContainerID,
		Hostname:    req.Hostname,
		Type:        jobType,
		AgentTask:   &taskReq,
		PrivateData: privateData,
	}, nil
}

func buildProfilingTracerArgs(
	taskReq *job.AgentTaskRequest,
	req *v1.CreateProfilingJobRequest,
) (job.JobType, error) {
	switch req.ProfilingType {
	case string(profiling.TypeCPU):
		language, err := profiling.ParseLanguage(req.Language)
		if err != nil || !profiling.IsSupported(language, profiling.TypeCPU) {
			return "", fmt.Errorf("cpu profiling not supported for %q", req.Language)
		}
		taskReq.TracerArgs = []string{"-t", string(profiling.TypeCPU)}
		if req.BinaryMatchPath != "" {
			taskReq.TracerArgs = append(
				taskReq.TracerArgs,
				"--binary-match-path", req.BinaryMatchPath,
			)
		}
		taskReq.TracerArgs = append(taskReq.TracerArgs, "-l", string(language))
		if err := appendProfilingToolPath(taskReq, req.ToolPath, language); err != nil {
			return "", err
		}
		return job.JobTypeProfilingCPU, nil
	case string(profiling.TypeMemory):
		language, err := profiling.ParseLanguage(req.Language)
		if err != nil || !profiling.IsSupported(language, profiling.TypeMemory) {
			return "", fmt.Errorf("memory profiling not supported for %q", req.Language)
		}
		mode, err := profiling.ParseMemoryMode(strings.ToLower(req.MemoryMode))
		if err != nil || !profiling.SupportsMemoryMode(language, mode) {
			return "", fmt.Errorf("memory mode not supported: %q", req.MemoryMode)
		}
		taskReq.TracerArgs = []string{
			"-t", string(profiling.TypeMemory),
			"--memory-mode", string(mode),
			"-l", string(language),
		}
		if err := appendProfilingToolPath(taskReq, req.ToolPath, language); err != nil {
			return "", err
		}
		return job.JobTypeProfilingMemory, nil
	case string(profiling.TypeLock):
		return buildLockProfilingTracerArgs(taskReq, req)
	default:
		return "", fmt.Errorf("unsupported profiling type %q", req.ProfilingType)
	}
}

func appendProfilingToolPath(
	taskReq *job.AgentTaskRequest,
	toolPath string,
	language profiling.Language,
) error {
	toolPath = strings.TrimSpace(toolPath)
	implementation, _ := profiling.ImplementationFor(language)
	if implementation != profiling.ImplementationNative && toolPath == "" {
		return fmt.Errorf("language %q requires tool_path", language)
	}
	if toolPath != "" {
		taskReq.TracerArgs = append(taskReq.TracerArgs, "--tool-path", toolPath)
	}
	return nil
}

func profilingTargetArgs(req *v1.CreateProfilingJobRequest) ([]string, error) {
	switch req.ProfilingType {
	case string(profiling.TypeCPU), string(profiling.TypeMemory),
		string(profiling.TypeLock):
	default:
		return nil, nil
	}
	language, err := profiling.ParseLanguage(req.Language)
	if err != nil {
		return nil, err
	}
	implementation, ok := profiling.ImplementationFor(language)
	if !ok {
		return nil, fmt.Errorf("profiling implementation not found for %q", language)
	}
	native := implementation == profiling.ImplementationNative
	hasNativeSelection := req.PID != 0 || req.ThreadGroup || len(req.CPUIDs) > 0
	if hasNativeSelection && !native {
		return nil, errors.New(
			"pid, cpu_ids, and thread_group are supported only by native profiling",
		)
	}

	targetArgs := make([]string, 0, 6)
	if native {
		if req.PID < 0 || int64(req.PID) > math.MaxInt32 {
			return nil, fmt.Errorf("pid must be between 1 and %d", math.MaxInt32)
		}
		if req.PID != 0 && req.ContainerID != "" {
			if req.ProfilingType == string(profiling.TypeLock) {
				return nil, errors.New(
					"lock profiling requires exactly one of pid or container_id",
				)
			}
			return nil, errors.New("pid and container_id are mutually exclusive")
		}
		if req.ThreadGroup && req.PID == 0 {
			return nil, errors.New("thread_group requires pid")
		}
		switch req.ProfilingType {
		case string(profiling.TypeMemory):
			if req.PID == 0 && req.ContainerID == "" {
				return nil, errors.New(
					"native memory profiling requires pid or container_id",
				)
			}
		case string(profiling.TypeLock):
			if req.PID == 0 && req.ContainerID == "" {
				return nil, errors.New(
					"lock profiling requires exactly one of pid or container_id",
				)
			}
			if req.ContainerID != "" {
				if err := pod.ValidateContainerID(req.ContainerID); err != nil {
					return nil, err
				}
			}
		}
	}

	if req.ContainerID != "" {
		targetArgs = append(targetArgs, "--container-id", req.ContainerID)
	}
	if !native {
		return targetArgs, nil
	}

	if req.PID != 0 {
		targetArgs = append(targetArgs, "--pid", strconv.Itoa(req.PID))
	}
	if req.ThreadGroup {
		targetArgs = append(targetArgs, "--thread-group")
	}
	if len(req.CPUIDs) == 0 {
		return targetArgs, nil
	}
	if req.ProfilingType != string(profiling.TypeCPU) {
		return nil, errors.New("cpu_ids are supported only by CPU profiling")
	}

	cpuIDs := append([]int(nil), req.CPUIDs...)
	sort.Ints(cpuIDs)
	for i, cpuID := range cpuIDs {
		if cpuID < 0 {
			return nil, fmt.Errorf("cpu_id must not be negative: %d", cpuID)
		}
		if i > 0 && cpuIDs[i-1] == cpuID {
			return nil, fmt.Errorf("duplicate cpu_id %d", cpuID)
		}
	}
	req.CPUIDs = cpuIDs
	values := make([]string, len(cpuIDs))
	for i, cpuID := range cpuIDs {
		values[i] = strconv.Itoa(cpuID)
	}
	targetArgs = append(targetArgs, "--cpuid", strings.Join(values, ","))
	return targetArgs, nil
}

func buildLockProfilingTracerArgs(
	taskReq *job.AgentTaskRequest,
	req *v1.CreateProfilingJobRequest,
) (job.JobType, error) {
	language, err := profiling.ParseLanguage(req.Language)
	if err != nil || !profiling.IsSupported(language, profiling.TypeLock) {
		return "", fmt.Errorf("lock profiling not supported for %q", req.Language)
	}
	if req.BinaryMatchPath != "" {
		return "", errors.New("binary_match_path is not supported by lock profiling")
	}
	if req.MemoryMode != "" {
		return "", errors.New("memory_mode is not supported by lock profiling")
	}

	mode := req.LockMode
	if mode == profiling.LockModeUnknown {
		mode = profiling.LockModeWaitTime
	}
	if mode != profiling.LockModeWaitTime {
		return "", fmt.Errorf("unsupported lock mode %q", mode)
	}
	lockType := req.LockType
	if lockType == profiling.LockTypeUnknown {
		lockType = profiling.LockTypeMutex
	}
	if _, err := profiling.ParseLockType(string(lockType)); err != nil {
		return "", err
	}

	threshold := strings.TrimSpace(req.LockWaitThreshold)
	if threshold == "" {
		threshold = defaultLockWaitThreshold
	}
	waitThreshold, err := time.ParseDuration(threshold)
	if err != nil {
		return "", fmt.Errorf("invalid lock_wait_threshold %q: %w", threshold, err)
	}
	if waitThreshold < 0 || waitThreshold > time.Hour {
		return "", errors.New("lock_wait_threshold must be between 0 and 1h")
	}

	taskReq.TracerArgs = []string{
		"-t", string(profiling.TypeLock),
		"-l", string(language),
	}
	taskReq.TracerArgs = append(
		taskReq.TracerArgs,
		"--lock-type", string(lockType),
		"--lock-wait-threshold", threshold,
	)
	req.LockMode = mode
	req.LockType = lockType
	req.LockWaitThreshold = threshold
	return job.JobTypeProfilingLock, nil
}

func newProfilingPrivateData(req *v1.CreateProfilingJobRequest) (json.RawMessage, error) {
	data, err := json.Marshal(profilingJobPrivateData{
		BinaryMatchPath:   req.BinaryMatchPath,
		ToolPath:          strings.TrimSpace(req.ToolPath),
		Duration:          req.Duration,
		Language:          req.Language,
		MemoryMode:        req.MemoryMode,
		CPUIDs:            req.CPUIDs,
		PID:               req.PID,
		ThreadGroup:       req.ThreadGroup,
		LockMode:          req.LockMode,
		LockType:          req.LockType,
		LockWaitThreshold: req.LockWaitThreshold,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding profiling private data: %w", err)
	}
	return data, nil
}

func parseProfilingJobListRequest(ctx *server.Context) (*profilingJobListRequest, error) {
	listParams, err := ctx.ParseListParams()
	if err != nil {
		return nil, err
	}

	var query profilingJobListQuery
	if err := ctx.ShouldBindQuery(&query); err != nil {
		return nil, fmt.Errorf("binding profiling job query: %w", err)
	}
	jobQuery, err := buildProfilingJobQuery(query)
	if err != nil {
		return nil, err
	}

	return &profilingJobListRequest{
		ListParams: listParams,
		JobQuery:   jobQuery,
	}, nil
}

func buildProfilingJobQuery(query profilingJobListQuery) (job.JobQuery, error) {
	if err := validateProfilingJobStatus(query.Status); err != nil {
		return job.JobQuery{}, err
	}

	jobQuery := job.JobQuery{
		ContainerID: query.ContainerID,
		Hostname:    query.Hostname,
		Status:      query.Status,
	}
	switch query.Type {
	case "":
		jobQuery.Types = []job.JobType{
			job.JobTypeProfilingMemory,
			job.JobTypeProfilingCPU,
			job.JobTypeProfilingLock,
		}
	case "cpu":
		jobQuery.Types = []job.JobType{job.JobTypeProfilingCPU}
	case "memory":
		jobQuery.Types = []job.JobType{job.JobTypeProfilingMemory}
	case "lock":
		jobQuery.Types = []job.JobType{job.JobTypeProfilingLock}
	default:
		return job.JobQuery{}, fmt.Errorf("invalid type %q", query.Type)
	}
	return jobQuery, nil
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

func parseProfilingJobID(ctx *server.Context) (string, error) {
	return validateProfilingJobID(ctx.Param("id"))
}

func validateProfilingJobID(id string) (string, error) {
	if id == "" {
		return "", errors.New("id is required")
	}
	return id, nil
}

func parsePatchProfilingJobRequest(ctx *server.Context) (*patchProfilingJobRequest, error) {
	id, err := parseProfilingJobID(ctx)
	if err != nil {
		return nil, err
	}

	var body v1.PatchStatusRequest
	if err := ctx.ShouldBindJSON(&body); err != nil {
		return nil, err
	}
	if body.Status != string(job.JobStatusStopped) {
		return nil, errors.New(`status must be "stopped"`)
	}

	return &patchProfilingJobRequest{
		ID:     id,
		Status: body.Status,
	}, nil
}
