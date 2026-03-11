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
	"sync"
	"time"

	"huatuo-bamai/internal/log"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	collectorMaxRequestsInFlight = 1000
)

var (
	collectorOnce        sync.Once
	collectorPromHandler http.Handler
)

// CollectorDo handles the prometheus requests.
func CollectorDo(ctx *gin.Context) {
	collectorOnce.Do(func() {
		collectorPromHandler = promhttp.HandlerFor(instance.promRegistry,
			promhttp.HandlerOpts{
				ErrorLog:            nil,
				ErrorHandling:       promhttp.ContinueOnError,
				MaxRequestsInFlight: collectorMaxRequestsInFlight,
				Timeout:             30 * time.Second,
			})

		log.Infof("The prometheus Metrics HTTP server is starting: %v", collectorPromHandler)
	})

	collectorPromHandler.ServeHTTP(ctx.Writer, ctx.Request)
}
