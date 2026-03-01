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

package services

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"huatuo-bamai/pkg/tracing"
)

type TracingJsonReq struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// handles the eventtracing list request.
func TracingList(ctx *gin.Context) {
	events := tracing.EventTracingStatus()

	resp := make([]TracingJsonReq, 0)
	for name, status := range events {
		resp = append(resp, TracingJsonReq{Name: name, Status: status})
	}

	ctx.JSON(http.StatusOK, resp)
}
