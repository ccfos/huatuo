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

package server

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func (s *Server) metricsHandler() ErrHandlerContextFunc {
	if s.promRegistry == nil {
		return func(ctx *Context) error {
			ctx.JSON(http.StatusNotImplemented, map[string]any{"status": "Prometheus registry not supported now"})
			return nil
		}
	}

	h := promhttp.HandlerFor(s.promRegistry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		Timeout:       30 * time.Second,
	})
	return func(ctx *Context) error {
		h.ServeHTTP(ctx.Writer(), ctx.Request())
		return nil
	}
}
