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
	"net/http"

	"huatuo-bamai/internal/request"

	"github.com/gin-gonic/gin"
)

// TracerStartReq is a request to start a case.
type TracerStartReq struct {
	Name string `json:"name"`
}

// TracerStopReq is a request to stop a case.
type TracerStopReq struct {
	Name string `json:"name"`
}

// TracerList shows all case info.
func TracerList(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, instance.tracingManager.Dump())
}

// TracerStart starts the special case.
func TracerStart(ctx *gin.Context) {
	startTracerReq := TracerStartReq{}
	if err := ctx.BindJSON(&startTracerReq); err != nil {
		ctx.JSON(http.StatusBadRequest, request.ErrorResponse{Message: err.Error()})
		return
	}

	if err := instance.tracingManager.StartByName(startTracerReq.Name); err != nil {
		ctx.JSON(http.StatusBadRequest, request.ErrorResponse{Message: err.Error()})
		return
	}

	ctx.JSON(http.StatusNoContent, nil)
}

// TracerStop stops the special case.
func TracerStop(ctx *gin.Context) {
	stopTracerReq := TracerStopReq{}
	if err := ctx.BindJSON(&stopTracerReq); err != nil {
		ctx.JSON(http.StatusBadRequest, request.ErrorResponse{Message: err.Error()})
		return
	}

	if err := instance.tracingManager.StopByName(stopTracerReq.Name); err != nil {
		ctx.JSON(http.StatusBadRequest, request.ErrorResponse{Message: err.Error()})
		return
	}

	ctx.JSON(http.StatusNoContent, nil)
}

// TracerStopAll stops all tracers.
func TracerStopAll(ctx *gin.Context) {
	if err := instance.tracingManager.Stop(); err != nil {
		ctx.JSON(http.StatusBadRequest, request.ErrorResponse{Message: err.Error()})
		return
	}

	ctx.JSON(http.StatusNoContent, nil)
}
