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
	"strconv"
	"strings"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/pkg/profiling"
)

type profilingJobListQuery struct {
	ContainerID string `form:"container_id"`
	Hostname    string `form:"hostname"`
	Status      string `form:"status"`
	Type        string `form:"type"`
}

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
	DurationSeconds   int                `json:"duration_seconds"`
	Language          string             `json:"language"`
	MemoryMode        string             `json:"memory_mode"`
	LockMode          profiling.LockMode `json:"lock_mode,omitempty"`
	LockType          profiling.LockType `json:"lock_type,omitempty"`
	LockWaitThreshold string             `json:"lock_wait_threshold,omitempty"`
	PID               int                `json:"pid,omitempty"`
	ThreadGroup       bool               `json:"thread_group,omitempty"`
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
		TracerName:  "profiler",
		DataType:    "db-json",
		ContainerID: req.ContainerID,
		Interval:    cfg.AggregationIntervalSeconds,
	}

	jobType, err := buildProfilingTracerArgs(&taskReq, req)
	if err != nil {
		return nil, err
	}
	if req.DurationSeconds < taskReq.Interval*2 {
		return nil, errors.New("duration_seconds must cover at least two profiling intervals")
	}
	if req.DurationSeconds+taskReq.Interval >= 3600 {
		return nil, errors.New("duration_seconds plus profiling interval must be less than 3600 seconds")
	}
	taskReq.TraceTimeout = req.DurationSeconds + taskReq.Interval

	// The job duration controls profiling lifetime while the agent task remains
	// alive long enough to be stopped externally.
	taskReq.Duration = req.DurationSeconds * 2
	taskReq.TracerArgs = append(
		taskReq.TracerArgs,
		"--duration", strconv.Itoa(req.DurationSeconds),
		"--aggr-interval", strconv.Itoa(taskReq.Interval),
		"--max-concurrent-procs", strconv.Itoa(cfg.MaxConcurrentProfilerProcesses),
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
		return job.JobTypeProfilingMemory, nil
	case string(profiling.TypeLock):
		language, err := profiling.ParseLanguage(req.Language)
		if err != nil || !profiling.IsSupported(language, profiling.TypeLock) {
			return "", fmt.Errorf("lock profiling not supported for %q", req.Language)
		}
		if req.PID < 0 {
			return "", errors.New("pid must be greater than zero")
		}
		hasPID := req.PID > 0
		hasContainer := req.ContainerID != ""
		if hasPID == hasContainer {
			return "", errors.New(
				"lock profiling requires exactly one pid or container_id",
			)
		}
		if req.ThreadGroup && !hasPID {
			return "", errors.New("thread_group requires pid")
		}

		lockMode := req.LockMode
		if lockMode == profiling.LockModeUnknown {
			lockMode = profiling.LockModeWaitTime
		}
		lockMode, err = profiling.ParseLockMode(string(lockMode))
		if err != nil {
			return "", err
		}
		lockType := req.LockType
		if lockType == profiling.LockTypeUnknown {
			lockType = profiling.LockTypeMutex
		}
		lockType, err = profiling.ParseLockType(string(lockType))
		if err != nil {
			return "", err
		}
		thresholdText := req.LockWaitThreshold
		if thresholdText == "" {
			thresholdText = "1us"
		}
		threshold, err := time.ParseDuration(thresholdText)
		if err != nil {
			return "", fmt.Errorf(
				"invalid lock_wait_threshold %q: %w",
				thresholdText,
				err,
			)
		}
		if threshold < time.Microsecond || threshold > time.Hour {
			return "", errors.New(
				"lock_wait_threshold must be between 1us and 1h",
			)
		}

		req.LockMode = lockMode
		req.LockType = lockType
		req.LockWaitThreshold = thresholdText
		taskReq.TracerArgs = []string{
			"-t", string(profiling.TypeLock),
			"--lock-type", string(lockType),
			"--lock-mode", string(lockMode),
			"--lock-wait-threshold", thresholdText,
			"-l", string(language),
		}
		if hasPID {
			taskReq.TracerArgs = append(
				taskReq.TracerArgs,
				"--pid", strconv.Itoa(req.PID),
			)
		} else {
			taskReq.TracerArgs = append(
				taskReq.TracerArgs,
				"--container-id", req.ContainerID,
			)
		}
		if req.ThreadGroup {
			taskReq.TracerArgs = append(taskReq.TracerArgs, "--thread-group")
		}
		return job.JobTypeProfilingLock, nil
	default:
		return "", fmt.Errorf("unsupported profiling type %q", req.ProfilingType)
	}
}

func newProfilingPrivateData(req *v1.CreateProfilingJobRequest) (json.RawMessage, error) {
	data, err := json.Marshal(profilingJobPrivateData{
		BinaryMatchPath:   req.BinaryMatchPath,
		DurationSeconds:   req.DurationSeconds,
		Language:          req.Language,
		MemoryMode:        req.MemoryMode,
		LockMode:          req.LockMode,
		LockType:          req.LockType,
		LockWaitThreshold: req.LockWaitThreshold,
		PID:               req.PID,
		ThreadGroup:       req.ThreadGroup,
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
