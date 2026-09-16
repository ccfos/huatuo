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

	"github.com/ccfos/huatuo/internal/auth"
	"github.com/ccfos/huatuo/internal/job"
	"github.com/ccfos/huatuo/pkg/observation"
	tracingdomain "github.com/ccfos/huatuo/pkg/tracing"
)

const defaultPageSize = 100

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
	if err := validateCreateInput(input); err != nil {
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
	currentJob, err := s.jobs.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if currentJob.Kind != job.KindTracing {
		return nil, job.ErrNotFound
	}
	if !principal.IsAdmin && currentJob.UserID != principal.ID {
		return nil, auth.ErrPermissionDenied
	}
	return currentJob, nil
}

// List returns one authorized Tracing Job page.
func (s *Service) List(
	ctx context.Context,
	principal auth.Principal,
	limit int,
	offset int,
) (*job.Page, error) {
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
	return s.jobs.Stop(ctx, requestID)
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

func validateCreateInput(input CreateInput) error {
	if input.Hostname == "" || strings.TrimSpace(input.Hostname) != input.Hostname {
		return errors.New("hostname must be a non-empty trimmed value")
	}
	if input.DurationSeconds > math.MaxInt64/int64(time.Second) {
		return errors.New("duration_seconds is outside the supported range")
	}
	return nil
}
