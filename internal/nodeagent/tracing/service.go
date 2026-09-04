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

// Package tracing implements the Node on-demand tracing service boundary.
package tracing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"huatuo-bamai/internal/nodeagent/operation"
	"huatuo-bamai/pkg/observation"
	tracingdomain "huatuo-bamai/pkg/tracing"
)

var (
	// ErrInvalidRequest indicates that tracing parameters are inconsistent.
	ErrInvalidRequest = errors.New("node tracing: invalid request")
	// ErrNotImplemented indicates that no tracing executor is available.
	ErrNotImplemented = errors.New("node tracing: service not implemented")
)

// StartRequest contains one on-demand tracing request.
type StartRequest struct {
	RequestID   string
	Duration    time.Duration
	Scope       observation.Scope
	ContainerID string
	Spec        tracingdomain.Spec
}

// Service validates tracing requests without exposing internal tools.
type Service struct {
	manager *operation.Manager
}

// NewService constructs the tracing protocol service.
func NewService(manager *operation.Manager) (*Service, error) {
	if manager == nil {
		return nil, errors.New("create node tracing service: operation manager is required")
	}
	return &Service{manager: manager}, nil
}

// Start validates the protocol but creates no placeholder operation while the
// executor is unavailable.
func (s *Service) Start(
	_ context.Context,
	request StartRequest,
) (operationSnapshot *operation.Operation, created bool, err error) {
	if err := validateRequest(request); err != nil {
		return nil, false, err
	}
	if existing, err := s.manager.GetByID(request.RequestID); err == nil {
		if existing.Kind == operation.KindTracing {
			return existing, false, nil
		}
		return nil, false, operation.ErrRequestIDConflict
	} else if !errors.Is(err, operation.ErrNotFound) {
		return nil, false, err
	}
	return nil, false, ErrNotImplemented
}

func validateRequest(request StartRequest) error {
	if request.RequestID == "" {
		return fmt.Errorf("%w: request ID is required", ErrInvalidRequest)
	}
	if request.Duration < time.Second || request.Duration%time.Second != 0 {
		return fmt.Errorf("%w: duration must be a whole positive number of seconds", ErrInvalidRequest)
	}
	if err := observation.ValidateScope(request.Scope, request.ContainerID); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if err := request.Spec.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if !tracingdomain.SupportsScope(request.Spec.Type, request.Scope) {
		return fmt.Errorf(
			"%w: scope %q is not supported for type %q",
			ErrInvalidRequest,
			request.Scope,
			request.Spec.Type,
		)
	}
	return nil
}
