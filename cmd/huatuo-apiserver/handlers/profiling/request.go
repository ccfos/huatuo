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

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/listing"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/pkg/profiling"
)

type profilingJobListQuery struct {
	ContainerID string `form:"containerID"`
	Hostname    string `form:"hostname"`
	Status      string `form:"status"`
	Type        string `form:"type"`
}

type profilingJobListRequest struct {
	ListParams server.ListParams
	JobQueries []job.JobQuery
}

type patchProfilingJobRequest struct {
	ID     string
	Status string
}

type profilingJobPrivateData struct {
	BinaryMatchPath string `json:"binary_match_path"`
	Duration        int    `json:"duration"`
	Language        string `json:"language"`
	MemoryMode      string `json:"memory_mode"`
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
	cfg *config.ProfilingConfig,
) (*job.CreateJobRequest, error) {
	taskReq := job.AgentTaskRequest{
		TracerName:   "profiler",
		DataType:     "db-json",
		Interval:     cfg.AggregationInterval,
		TraceTimeout: cfg.ExecutionTimeout,
	}

	jobType, err := buildProfilingTracerArgs(&taskReq, req)
	if err != nil {
		return nil, err
	}
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
) (string, error) {
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
		return ProfilingCPU, nil
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
		return ProfilingMemory, nil
	default:
		return "", fmt.Errorf("unsupported profiling type %q", req.ProfilingType)
	}
}

func newProfilingPrivateData(req *v1.CreateProfilingJobRequest) (json.RawMessage, error) {
	data, err := json.Marshal(profilingJobPrivateData{
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

func parseProfilingJobListRequest(ctx *server.Context) (*profilingJobListRequest, error) {
	listParams, err := ctx.ParseListParams()
	if err != nil {
		return nil, err
	}

	var query profilingJobListQuery
	if err := ctx.ShouldBindQuery(&query); err != nil {
		return nil, fmt.Errorf("binding profiling job query: %w", err)
	}
	jobQueries, err := buildProfilingJobQueries(query)
	if err != nil {
		return nil, err
	}

	return &profilingJobListRequest{
		ListParams: listParams,
		JobQueries: jobQueries,
	}, nil
}

func buildProfilingJobQueries(query profilingJobListQuery) ([]job.JobQuery, error) {
	if err := validateProfilingJobStatus(query.Status); err != nil {
		return nil, err
	}

	jobQuery := job.JobQuery{
		ContainerID: query.ContainerID,
		Hostname:    query.Hostname,
		Status:      query.Status,
	}
	switch query.Type {
	case "":
		memoryQuery := jobQuery
		memoryQuery.Type = ProfilingMemory
		cpuQuery := jobQuery
		cpuQuery.Type = ProfilingCPU
		return []job.JobQuery{memoryQuery, cpuQuery}, nil
	case "cpu":
		jobQuery.Type = ProfilingCPU
		return []job.JobQuery{jobQuery}, nil
	case "memory":
		jobQuery.Type = ProfilingMemory
		return []job.JobQuery{jobQuery}, nil
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
	if body.Status != listing.StatusStopped {
		return nil, errors.New(`status must be "stopped"`)
	}

	return &patchProfilingJobRequest{
		ID:     id,
		Status: body.Status,
	}, nil
}
