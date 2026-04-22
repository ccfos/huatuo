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

package config

import (
	"net/http"
	"reflect"

	appconfig "huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/request"

	"github.com/gin-gonic/gin"
)

// Request set params for tracer
type Request struct {
	Config map[string]any `json:"config"`
}

// Config set config param and sync to file
func Config(ctx *gin.Context) {
	req := Request{}
	if err := ctx.BindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, request.ErrorResponse{Message: err.Error()})
		return
	}

	for k, v := range req.Config {
		if reflect.ValueOf(v).Kind() == reflect.Float64 {
			v = int(v.(float64))
		}
		appconfig.Set(k, v)
	}
	if err := appconfig.Sync(); err != nil {
		log.Warnf("config sync error: %v", err)
		ctx.JSON(http.StatusInternalServerError, request.ErrorResponse{Message: err.Error()})
		return
	}
	ctx.JSON(http.StatusNoContent, nil)
}
