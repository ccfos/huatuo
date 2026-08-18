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

	"huatuo-bamai/internal/log"
)

func (s *Server) healthzHandler() ErrHandlerContextFunc {
	return func(ctx *Context) error {
		ctx.Status(http.StatusNoContent)
		return nil
	}
}

func (s *Server) readyzHandler() ErrHandlerContextFunc {
	return func(ctx *Context) error {
		if s.config.Ready == nil {
			ctx.Status(http.StatusNoContent)
			return nil
		}
		if err := s.config.Ready(ctx.Request().Context()); err != nil {
			log.WithError(err).Warn("readiness check failed")
			ctx.JSON(http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
			return nil
		}
		ctx.Status(http.StatusNoContent)
		return nil
	}
}
