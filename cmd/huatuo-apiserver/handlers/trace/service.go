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

package trace

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"huatuo-bamai/internal/auth"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/pkg/observation"
	tracingdomain "huatuo-bamai/pkg/tracing"
)

const (
	defaultPageSize = 100
	maxPageSize     = 1000
)

// CreateInput contains transport-independent Tracing Job parameters.
type CreateInput struct {
	Hostname        string
	DurationSeconds int64
	Scope           observation.Scope
	ContainerID     string
	Spec            tracingdomain.Spec
}

// Service owns Tracing authorization, validation, and Job commands.
type Service struct {
	jobs *job.Manager
}

// NewService constructs the Tracing application service.
func NewService(jobs *job.Manager) (*Service, error) {
	if jobs == nil {
		return nil, errors.New("create Tracing service: Job Manager is required")
	}
	return &Service{jobs: jobs}, nil
}

// Create validates and persists one independent Tracing Job.
func (s *Service) Create(
	ctx context.Context,
	principal auth.Principal,
	input CreateInput,
) (*job.Job, error) {
	if err := validateCreateInput(principal, input); err != nil {
		return nil, fmt.Errorf("%w: %w", job.ErrInvalidQuery, err)
	}
	return s.jobs.Create(ctx, &job.CreateRequest{
		UserID:      principal.ID,
		Hostname:    input.Hostname,
		Duration:    time.Duration(input.DurationSeconds) * time.Second,
		Scope:       input.Scope,
		ContainerID: input.ContainerID,
		Spec:        job.Spec{Tracing: &input.Spec},
	})
}

// Get returns one authorized Tracing Job.
func (s *Service) Get(
	ctx context.Context,
	principal auth.Principal,
	requestID string,
) (*job.Job, error) {
	jobEntity, err := s.jobs.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if jobEntity.Kind != job.KindTracing {
		return nil, job.ErrNotFound
	}
	if !principal.IsAdmin && jobEntity.UserID != principal.ID {
		return nil, auth.ErrPermissionDenied
	}
	return jobEntity, nil
}

// List returns one authorized Tracing Job page.
func (s *Service) List(
	ctx context.Context,
	principal auth.Principal,
	limit int,
	offset int,
) (*job.Page, error) {
	if err := validatePage(limit, offset); err != nil {
		return nil, err
	}
	return s.jobs.ListPage(ctx, &job.Query{
		UserID:  principal.ID,
		IsAdmin: principal.IsAdmin,
		Kinds:   []job.Kind{job.KindTracing},
		Sort:    "-created_at",
		Limit:   limit,
		Offset:  offset,
	})
}

// Stop persists a user stop intent and returns the resulting Job snapshot.
func (s *Service) Stop(
	ctx context.Context,
	principal auth.Principal,
	requestID string,
) (*job.Job, error) {
	if _, err := s.Get(ctx, principal, requestID); err != nil {
		return nil, err
	}
	if err := s.jobs.Stop(ctx, requestID); err != nil {
		return nil, err
	}
	return s.jobs.Get(ctx, requestID)
}

// Capabilities returns the versioned static Tracing capability table.
func (*Service) Capabilities() []tracingdomain.Capability {
	return tracingdomain.Capabilities()
}

// NormalizePage applies the public pagination defaults.
func NormalizePage(limit, offset *int) (int, int) {
	normalizedLimit := defaultPageSize
	if limit != nil {
		normalizedLimit = *limit
	}
	normalizedOffset := 0
	if offset != nil {
		normalizedOffset = *offset
	}
	return normalizedLimit, normalizedOffset
}

func validateCreateInput(principal auth.Principal, input CreateInput) error {
	if principal.ID == "" {
		return errors.New("authenticated user ID is required")
	}
	if input.Hostname == "" || strings.TrimSpace(input.Hostname) != input.Hostname {
		return errors.New("hostname must be a non-empty trimmed value")
	}
	if input.DurationSeconds <= 0 || input.DurationSeconds > math.MaxInt64/int64(time.Second) {
		return errors.New("duration_seconds is outside the supported range")
	}
	if err := observation.ValidateScope(input.Scope, input.ContainerID); err != nil {
		return err
	}
	if err := input.Spec.Validate(); err != nil {
		return err
	}
	if !tracingdomain.SupportsScope(input.Spec.Type, input.Scope) {
		return fmt.Errorf("tracing type %q does not support scope %q", input.Spec.Type, input.Scope)
	}
	return nil
}

func validatePage(limit, offset int) error {
	if limit <= 0 || limit > maxPageSize {
		return fmt.Errorf("%w: limit must be between 1 and %d", job.ErrInvalidQuery, maxPageSize)
	}
	if offset < 0 {
		return fmt.Errorf("%w: offset must not be negative", job.ErrInvalidQuery)
	}
	return nil
}
