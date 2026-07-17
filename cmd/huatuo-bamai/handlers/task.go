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

package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/tracing"

	"github.com/go-playground/validator/v10"
)

type TaskHandler struct {
	Handlers []server.Handle
}

func NewTaskHandler() *TaskHandler {
	h := &TaskHandler{}
	h.Handlers = []server.Handle{
		{Typ: server.HttpPost, Uri: "", Handle: h.create},
		{Typ: server.HttpGet, Uri: "", Handle: h.list},
		{Typ: server.HttpGet, Uri: "/:id", Handle: h.get},
		{Typ: server.HttpDelete, Uri: "/:id", Handle: h.stop},
	}
	return h
}

type NewTaskReq struct {
	TracerName        string   `json:"tracer_name" binding:"required"`
	Timeout           int      `json:"timeout" binding:"required,number,lt=3600"`
	DataType          string   `json:"data_type" binding:"required"`
	ContainerID       string   `json:"container_id,omitempty"`
	ContainerHostname string   `json:"container_hostname,omitempty"`
	TracerArgs        []string `json:"trace_args" binding:"omitempty"`
}

var containerByHostname = pod.ContainerByHostname

func handleBindError(ctx *server.Context, err error) {
	var validationError *validator.ValidationErrors
	if errors.As(err, &validationError) {
		response.ErrorWithCode(ctx, http.StatusBadRequest, 400, (*validationError)[0].Namespace())
		return
	}
	response.ErrorWithCode(ctx, http.StatusBadRequest, 400, err.Error())
}

func (h *TaskHandler) create(ctx *server.Context) error {
	var req NewTaskReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		handleBindError(ctx, err)
		return nil
	}

	if tracing.RunningTaskCount() > config.Get().Task.MaxRunningTask {
		return response.ErrInvalidRequest.WithMessage("too many running tasks")
	}

	storageDefault := tracing.TaskStorageDB
	if req.DataType == "json" {
		storageDefault = tracing.TaskStorageStdout
	}

	taskID, err := tracing.AllocTaskID()
	if err != nil {
		return response.ErrInternal.WithMessage("failed to allocate task id")
	}
	tracerArgs, err := taskTracerArgs(&req, taskID)
	if err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	id := tracing.NewTaskWithID(taskID, req.TracerName, time.Duration(req.Timeout)*time.Second, storageDefault, tracerArgs)
	if id == "" {
		return response.ErrInternal.WithMessage("failed to allocate task id")
	}
	response.Success(ctx, map[string]any{"task_id": id})
	return nil
}

func taskTracerArgs(req *NewTaskReq, tracerID string) ([]string, error) {
	args := append([]string(nil), req.TracerArgs...)
	if req.TracerName != "profiler" {
		return args, nil
	}

	if !hasCLIFlag(args, "--container-id") {
		containerID := req.ContainerID
		if containerID == "" && req.ContainerHostname != "" {
			// The public API historically describes this selector as hostname or
			// ID. Preserve both forms while always passing the stable ID to the
			// profiler's cgroup resolver.
			if pod.ValidateContainerID(req.ContainerHostname) == nil {
				containerID = req.ContainerHostname
			} else {
				container, err := containerByHostname(req.ContainerHostname)
				if err != nil {
					return nil, fmt.Errorf("resolve container hostname %q: %w", req.ContainerHostname, err)
				}
				if container == nil {
					return nil, fmt.Errorf("container hostname %q not found", req.ContainerHostname)
				}
				containerID = container.ID
			}
		}
		if containerID != "" {
			args = append(args, "--container-id", containerID)
		}
	}
	if tracerID != "" && !hasCLIFlag(args, "--tracer-id") {
		args = append(args, "--tracer-id", tracerID)
	}
	return args, nil
}

func hasCLIFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func (h *TaskHandler) list(ctx *server.Context) error {
	response.Success(ctx, map[string]any{"tasks": tracing.ListTasks()})
	return nil
}

func (h *TaskHandler) get(ctx *server.Context) error {
	id := ctx.Param("id")
	if id == "" {
		return response.ErrInvalidRequest.WithMessage("missing task id")
	}

	result := tracing.Result(id)
	responseData := map[string]any{"status": result.TaskStatus}
	switch result.TaskStatus {
	case tracing.StatusCompleted:
		responseData["data"] = string(result.TaskData)
	case tracing.StatusNotExist, tracing.StatusFailed:
		responseData["error"] = result.TaskErr.Error()
	}

	response.Success(ctx, responseData)
	return nil
}

func (h *TaskHandler) stop(ctx *server.Context) error {
	taskID := ctx.Param("id")
	if taskID == "" {
		return response.ErrInvalidRequest.WithMessage("missing task id")
	}

	if err := tracing.StopTask(taskID); err != nil {
		if errors.Is(err, tracing.ErrTaskNotFound) {
			return response.ErrNotFound.WithMessage("task not found")
		}
		return response.ErrInternal.WithMessage(err.Error())
	}

	ctx.Status(http.StatusNoContent)
	return nil
}
