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
	"time"

	"huatuo-bamai/internal/server"
	"huatuo-bamai/pkg/tracing"

	"github.com/prometheus/client_golang/prometheus"
)

// Start starts the HTTP server with all handlers registered.
func Start(addr string, mgrTracing *tracing.TracingManager, promReg *prometheus.Registry) {
	s := server.NewServer(&server.Config{
		EnablePProf:     true,
		EnableRateLimit: true,
		RateLimit:       200,
		RateBurst:       200,
		EnableRetry:     true,
		PromReg:         promReg,
	})

	SetTracingManager(mgrTracing)

	s.MustRegisterRoutes("/tasks", NewTaskHandler().Handlers)
	s.MustRegisterRoutes("/tracers", NewTracerHandler(mgrTracing).Handlers)
	s.MustRegisterRoutes("", NewContainerHandler().Handlers)
	s.MustRegisterRoutes("", NewConfigHandler().Handlers)

	_ = s.Run(&server.Option{
		Addr:          addr,
		RetryMaxTime:  5 * time.Minute,
		RetryInterval: 1 * time.Minute,
	})
}
