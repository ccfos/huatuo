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

package result

import (
	"context"
	"errors"
	"fmt"

	"huatuo-bamai/internal/job"
)

const maxRawProfilePageSize = 100

type jobReader interface {
	Get(ctx context.Context, jobID string) (*job.Job, error)
}

// Service enforces Job ownership and terminal-state result access.
type Service struct {
	jobs       jobReader
	repository Repository
}

// IsPublished checks the durable commit marker for an already authorized Job.
func (s *Service) IsPublished(
	ctx context.Context,
	jobEntity *job.Job,
) (bool, error) {
	if jobEntity == nil || jobEntity.ID == "" {
		return false, fmt.Errorf("%w: Job is required", job.ErrInvalidQuery)
	}
	if jobEntity.Kind != job.KindProfiling {
		return false, ErrWrongKind
	}
	if jobEntity.Status != job.StatusCompleted && jobEntity.Status != job.StatusOutcomeUnknown {
		return false, ErrUnavailable
	}
	return s.repository.IsPublished(ctx, jobEntity.ID)
}

// NewService constructs a Profiling result query boundary.
func NewService(jobs jobReader, repository Repository) (*Service, error) {
	if jobs == nil {
		return nil, errors.New("create profiling result service: Job reader is required")
	}
	if repository == nil {
		return nil, errors.New("create profiling result service: repository is required")
	}
	return &Service{jobs: jobs, repository: repository}, nil
}

// List returns published Profiles after validating the owning Job.
func (s *Service) List(
	ctx context.Context,
	requestID string,
	userID string,
	isAdmin bool,
	limit int,
	offset int,
) (*Page, error) {
	if requestID == "" {
		return nil, fmt.Errorf("%w: request ID is required", job.ErrInvalidQuery)
	}
	if limit <= 0 || limit > maxRawProfilePageSize {
		return nil, fmt.Errorf(
			"%w: limit must be between 1 and %d",
			job.ErrInvalidQuery,
			maxRawProfilePageSize,
		)
	}
	if offset < 0 {
		return nil, fmt.Errorf("%w: offset must not be negative", job.ErrInvalidQuery)
	}
	jobEntity, err := s.authorizeResult(ctx, requestID, userID, isAdmin)
	if err != nil {
		return nil, err
	}
	published, err := s.IsPublished(ctx, jobEntity)
	if err != nil {
		return nil, err
	}
	if !published {
		return nil, fmt.Errorf(
			"%w: profiling result %q has no publication marker",
			ErrNotFound,
			requestID,
		)
	}

	profiles, err := s.repository.List(ctx, requestID, limit+1, offset)
	if err != nil {
		return nil, err
	}
	hasMore := len(profiles) > limit
	if hasMore {
		profiles = profiles[:limit]
	}
	return &Page{Items: profiles, Limit: limit, Offset: offset, HasMore: hasMore}, nil
}

func (s *Service) authorizeResult(
	ctx context.Context,
	requestID string,
	userID string,
	isAdmin bool,
) (*job.Job, error) {
	jobEntity, err := s.jobs.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if jobEntity.Kind != job.KindProfiling {
		return nil, ErrWrongKind
	}
	if !isAdmin && jobEntity.UserID != userID {
		return nil, ErrForbidden
	}
	switch jobEntity.Status {
	case job.StatusPending, job.StatusRunning, job.StatusStopping:
		return nil, ErrNotReady
	case job.StatusStopped, job.StatusFailed:
		return nil, ErrUnavailable
	case job.StatusCompleted, job.StatusOutcomeUnknown:
	default:
		return nil, fmt.Errorf("unsupported Job status %q", jobEntity.Status)
	}
	return jobEntity, nil
}
