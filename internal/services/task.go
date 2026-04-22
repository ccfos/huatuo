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

package services

import (
	"errors"
	"net/http"
	"time"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/pkg/tracing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// NewTaskReq represents the structure of the request body for creating a new task.
type NewTaskReq struct {
	TracerName string   `json:"tracer_name" binding:"required"`            // Name of the tracer, required field
	Timeout    int      `json:"timeout" binding:"required,number,lt=3600"` // Timeout in seconds, must be less than 3600s(1 hour)
	DataType   string   `json:"data_type" binding:"required"`              // Type of data to be handled, required field
	TracerArgs []string `json:"trace_args" binding:"omitempty"`            // Additional arguments for the tracer, optional field
}

// StopTaskReq represents the structure of the request body for stopping a task.
type StopTaskReq struct {
	TaskID string `json:"id" binding:"required"` // Identifier of the task to stop, required field
}

func handleBindError(ctx *gin.Context, err error) {
	var validationError *validator.ValidationErrors

	if errors.As(err, &validationError) {
		ctx.JSON(400, gin.H{"body invalid": (*validationError)[0].Namespace()})
		return
	}

	ctx.JSON(400, gin.H{"error": err.Error()})
}

// NewTask creates a new task based on the provided request.
func NewTask(ctx *gin.Context) {
	var req NewTaskReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		handleBindError(ctx, err)
		return
	}

	if tracing.RunningTaskCount() > config.Get().Task.MaxRunningTask {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "too many running tasks"})
		return
	}

	storageDefault := tracing.TaskStorageDB
	if req.DataType == "json" {
		storageDefault = tracing.TaskStorageStdout
	}

	id := tracing.NewTask(req.TracerName, time.Duration(req.Timeout)*time.Second, storageDefault, req.TracerArgs)

	ctx.JSON(200, gin.H{"task_id": id})
}

// TaskResult retrieves the result of a task.
func TaskResult(ctx *gin.Context) {
	id := ctx.Query("id")
	if id == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "missing task id"})
		return
	}

	result := tracing.Result(id)

	response := gin.H{"status": result.TaskStatus}
	httpStatus := http.StatusOK

	switch result.TaskStatus {
	case tracing.StatusCompleted:
		response["data"] = string(result.TaskData)
	case tracing.StatusNotExist, tracing.StatusFailed:
		response["error"] = result.TaskErr.Error()
	}
	ctx.JSON(httpStatus, response)
}

// TaskStop stops a running task.
func TaskStop(ctx *gin.Context) {
	var req StopTaskReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		handleBindError(ctx, err)
		return
	}
	if err := tracing.StopTask(req.TaskID); err != nil {
		if errors.Is(err, tracing.ErrTaskNotFound) {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "task not found"})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"status": "ok"})
}
