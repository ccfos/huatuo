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
	"errors"
	"fmt"

	serverapi "huatuo-bamai/apis/v1/server"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/profiling"
	"huatuo-bamai/cmd/huatuo-apiserver/handlers/trace"
	"huatuo-bamai/internal/job"
	profileservice "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/internal/profiling/publication"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/internal/version"

	httpGin "github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

// ServerOptions groups the dependencies required to start the API server.
type ServerOptions struct {
	Addr                string
	PromReg             *prometheus.Registry
	JobManager          *job.Manager
	ProfileStorage      *profileservice.ProfileStorage
	ProfileQueryService *profileservice.ProfileQueryService
	Publications        *publication.Store
	ProfilingConfig     profiling.Config
	AuthUsers           []server.UserConfig
	EnablePProf         bool
	VersionInfo         *version.Info
	RateLimit           *server.RateLimitConfig
}

// Start starts the API service with generated business routes.
func Start(opts *ServerOptions) (*server.Server, error) {
	if opts == nil {
		return nil, errors.New("start API server: options are required")
	}
	if opts.JobManager == nil {
		return nil, errors.New("start API server: Job Manager is required")
	}
	if len(opts.AuthUsers) == 0 {
		return nil, errors.New("start API server: at least one auth user is required")
	}

	if (opts.ProfileStorage == nil) != (opts.Publications == nil) {
		return nil, errors.New(
			"start API server: profile storage and publication store must be configured together",
		)
	}
	var rawProfiles profiling.RawProfileReader
	var publications profiling.PublicationReader
	if opts.ProfileStorage != nil {
		rawProfiles = opts.ProfileStorage
		publications = opts.Publications
	}
	profilingService, err := profiling.NewService(
		opts.JobManager,
		rawProfiles,
		publications,
		opts.ProfilingConfig,
	)
	if err != nil {
		return nil, err
	}
	tracingService, err := trace.NewService(opts.JobManager)
	if err != nil {
		return nil, err
	}
	apiHandler, err := NewAPIHandler(
		profilingService,
		tracingService,
		opts.ProfileQueryService,
	)
	if err != nil {
		return nil, err
	}

	httpServer := server.NewServer(&server.Config{
		EnablePProf: opts.EnablePProf,
		RateLimit:   opts.RateLimit,
		AuthUsers:   opts.AuthUsers,
		PublicPaths: []string{"/openapi.json", "/readyz"},
		AdminPaths: []string{
			"/v1/profiling/flamegraph/**",
		},
		PromReg:     opts.PromReg,
		VersionInfo: opts.VersionInfo,
		ErrorStatusMapper: response.ChainHTTPStatusMappers(
			serverapi.HTTPStatusForErrorCode,
			response.LegacyHTTPStatusForErrorCode,
		),
	})

	errorHandlers := httpServer.StrictErrorHandlers()
	strictHandler := serverapi.NewStrictHandlerWithOptions(
		apiHandler,
		nil,
		serverapi.StrictGinServerOptions{
			RequestErrorHandlerFunc:  errorHandlers.RequestError,
			HandlerErrorFunc:         errorHandlers.HandlerError,
			ResponseErrorHandlerFunc: errorHandlers.ResponseError,
		},
	)
	if err := httpServer.RegisterOpenAPIHandlers(
		serverapi.OpenAPIJSON(),
		func(router httpGin.IRouter) {
			serverapi.RegisterHandlers(router, strictHandler)
		},
	); err != nil {
		return nil, fmt.Errorf("register Server API handlers: %w", err)
	}

	if err := httpServer.Start(opts.Addr); err != nil {
		return nil, err
	}
	return httpServer, nil
}
