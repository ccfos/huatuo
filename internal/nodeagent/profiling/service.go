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

// Package profiling adapts typed profiling requests to Node operations.
package profiling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/exec"
	"huatuo-bamai/internal/nodeagent/operation"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"
)

var (
	// ErrInvalidRequest indicates that profiling parameters are inconsistent.
	ErrInvalidRequest = errors.New("node profiling: invalid request")
	// ErrEnvironmentUnsupported indicates that this node cannot run a valid request.
	ErrEnvironmentUnsupported = errors.New("node profiling: environment unsupported")
)

// Config contains Node-local profiler implementation settings.
type Config struct {
	ProfilerPath            string
	ToolstreamSocketPath    string
	NodeAPIAddress          string
	JavaToolPath            string
	PythonToolPath          string
	AggregationInterval     time.Duration
	MaxConcurrentProcesses  int
	CommandOutputLimitBytes int
	ToolstreamServer        *toolstream.Server
	ResultPublisher         ResultPublisher
}

// ResultPublisher owns the durable result commit marker.
type ResultPublisher interface {
	Publish(ctx context.Context, requestID string) error
}

// StartRequest contains one validated profiling operation request.
type StartRequest struct {
	RequestID   string
	Duration    time.Duration
	Scope       observation.Scope
	ContainerID string
	Spec        profilingdomain.Spec
}

// Service validates profiling requests and delegates lifecycle ownership.
type Service struct {
	manager *operation.Manager
	config  Config
}

// NewService constructs a profiling service without starting tools.
func NewService(manager *operation.Manager, config *Config) (*Service, error) {
	if manager == nil {
		return nil, errors.New("create node profiling service: operation manager is required")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return &Service{manager: manager, config: *config}, nil
}

// Start validates the node environment before registering an operation.
func (s *Service) Start(
	_ context.Context,
	request *StartRequest,
) (operationSnapshot *operation.Operation, created bool, err error) {
	if err := validateRequest(request); err != nil {
		return nil, false, err
	}
	if existing, err := s.manager.GetByID(request.RequestID); err == nil {
		if existing.Kind == operation.KindProfiling {
			return existing, false, nil
		}
		return nil, false, operation.ErrRequestIDConflict
	} else if !errors.Is(err, operation.ErrNotFound) {
		return nil, false, err
	}
	if err := validateEnvironment(request, &s.config); err != nil {
		return nil, false, err
	}

	commandSpec, err := buildCommand(request, &s.config)
	if err != nil {
		return nil, false, err
	}
	process, err := exec.New(commandSpec)
	if err != nil {
		return nil, false, fmt.Errorf("%w: build profiler process: %w", ErrInvalidRequest, err)
	}

	return s.manager.Start(operation.StartRequest{
		RequestID: request.RequestID,
		Kind:      operation.KindProfiling,
		Executor: newExecutor(
			process,
			s.config.ToolstreamServer,
			s.config.ResultPublisher,
			request.RequestID,
		),
	})
}
