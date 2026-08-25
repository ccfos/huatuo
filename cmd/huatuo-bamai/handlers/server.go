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
	"fmt"

	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/cmd/huatuo-bamai/config"
	nodeprofiling "huatuo-bamai/internal/nodeagent/profiling"
	nodetracing "huatuo-bamai/internal/nodeagent/tracing"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/internal/version"

	httpGin "github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

// ServerOptions groups the dependencies required to start the HTTP server.
type ServerOptions struct {
	Addr             string
	BearerToken      string
	ProfilingService *nodeprofiling.Service
	TracingService   *nodetracing.Service
	PromReg          *prometheus.Registry
	VersionInfo      *version.Info
}

// Start starts the HTTP server with all handlers registered.
func Start(opts ServerOptions) (*server.Server, error) {
	nodeHandler, err := NewNodeAPIHandler(opts.ProfilingService, opts.TracingService)
	if err != nil {
		return nil, err
	}
	s := server.NewServer(&server.Config{
		EnablePProf:         true,
		DisableHealthRoutes: true,
		RateLimit: &server.RateLimitConfig{
			RequestsPerSecond: 200,
			Burst:             200,
		},
		EnableRetry: true,
		RequireAuth: true,
		AuthUsers: []server.UserConfig{{
			ID:          nodePrincipalID,
			BearerToken: opts.BearerToken,
			Permissions: []string{"/**"},
		}},
		PublicPaths: []string{
			"/openapi.json",
			// Profiler subprocesses still use this legacy local metadata route.
			"/containers/json",
		},
		PromReg:     opts.PromReg,
		VersionInfo: opts.VersionInfo,
		ErrorStatusMapper: response.ChainHTTPStatusMappers(
			nodeapi.HTTPStatusForErrorCode,
			response.LegacyHTTPStatusForErrorCode,
		),
	})

	s.MustRegisterRoutes("", NewContainerHandler().Handlers)
	s.MustRegisterRoutes("", NewConfigHandler().Handlers)
	httpConfig := config.Get().HTTPServer
	s.MustRegisterRoutes(
		"/v1/events",
		NewEventsHandler(
			httpConfig.MaxEventStreamClients,
			httpConfig.EventStreamKeepAliveIntervalSeconds,
		).Handlers,
	)

	errorHandlers := s.StrictErrorHandlers()
	strictHandler := nodeapi.NewStrictHandlerWithOptions(
		nodeHandler,
		nil,
		nodeapi.StrictGinServerOptions{
			RequestErrorHandlerFunc:  errorHandlers.RequestError,
			HandlerErrorFunc:         errorHandlers.HandlerError,
			ResponseErrorHandlerFunc: errorHandlers.ResponseError,
		},
	)
	if err := s.RegisterOpenAPIHandlers(nodeapi.OpenAPIJSON(), func(router httpGin.IRouter) {
		nodeapi.RegisterHandlers(router, strictHandler)
	}); err != nil {
		return nil, fmt.Errorf("register Node API handlers: %w", err)
	}

	if err := s.Start(opts.Addr); err != nil {
		return nil, err
	}

	return s, nil
}
