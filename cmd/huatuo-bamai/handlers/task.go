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

package handlers

import (
	"errors"
	"net/http"
	"time"

	"huatuo-bamai/cmd/huatuo-bamai/config"
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
		{Typ: server.HttpGet, Uri: "/:id", Handle: h.get},
		{Typ: server.HttpDelete, Uri: "/:id", Handle: h.stop},
	}
	return h
}

type NewTaskReq struct {
	TracerName string   `json:"tracer_name" binding:"required"`
	Timeout    int      `json:"timeout" binding:"required,number,lt=3600"`
	DataType   string   `json:"data_type" binding:"required"`
	TracerArgs []string `json:"trace_args" binding:"omitempty"`
}

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

	id := tracing.NewTask(req.TracerName, time.Duration(req.Timeout)*time.Second, storageDefault, req.TracerArgs)
	response.Success(ctx, map[string]any{"task_id": id})
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
