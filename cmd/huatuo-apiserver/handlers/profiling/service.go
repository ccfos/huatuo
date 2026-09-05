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

package profiling

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"huatuo-bamai/internal/auth"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/profiling/publication"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"
	profilingstore "huatuo-bamai/pkg/profiling/store"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

const (
	defaultPageSize           = 100
	defaultRawProfilePageSize = 20
)

var (
	// ErrResultNotReady indicates that the Profiling Job is still active.
	ErrResultNotReady = errors.New("profiling result is not ready")
	// ErrResultNotFound indicates that no durable publication marker exists.
	ErrResultNotFound = errors.New("profiling result is not found")
	// ErrResultUnavailable indicates that the Job cannot provide a complete result.
	ErrResultUnavailable = errors.New("profiling result is unavailable")
	// ErrResultStoreUnavailable indicates that result storage could not serve a query.
	ErrResultStoreUnavailable = errors.New("profiling result store is unavailable")
)

// Config contains Profiling response configuration.
type Config struct {
	DashboardBaseURL string
}

// CreateInput contains transport-independent Profiling Job parameters.
type CreateInput struct {
	Hostname        string
	DurationSeconds int64
	Scope           observation.Scope
	ContainerID     string
	Spec            profilingdomain.Spec
}

// RawProfile is one stored profiling window without storage implementation fields.
type RawProfile struct {
	Hostname          string
	Region            string
	UploadedTimestamp time.Time
	StartedTimestamp  time.Time
	ContainerID       string
	ContainerHostname string
	ContainerType     string
	ContainerQoS      string
	ProfileType       string
	Profile           *profilev1.Profile
}

// RawProfilePage contains one page of published profiling windows.
type RawProfilePage struct {
	Items   []*RawProfile
	Limit   int
	Offset  int
	HasMore bool
}

// Service owns Profiling authorization, validation, Job commands, and result access.
type Service struct {
	jobs             *job.Manager
	profileStore     *profilingstore.Store
	publications     *publication.Store
	dashboardBaseURL string
}

// NewService constructs the Profiling application service.
func NewService(
	jobs *job.Manager,
	profileStore *profilingstore.Store,
	publications *publication.Store,
	config Config,
) (*Service, error) {
	if jobs == nil {
		return nil, errors.New("create profiling service: job manager is required")
	}
	if (profileStore == nil) != (publications == nil) {
		return nil, errors.New(
			"create profiling service: profile storage and publication store must be configured together",
		)
	}
	return &Service{
		jobs:             jobs,
		profileStore:     profileStore,
		publications:     publications,
		dashboardBaseURL: config.DashboardBaseURL,
	}, nil
}

// Create validates and persists one independent Profiling Job.
func (s *Service) Create(
	ctx context.Context,
	principal auth.Principal,
	input *CreateInput,
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
		Spec:        job.Spec{Profiling: &input.Spec},
	})
}

// Get returns one authorized Profiling Job.
func (s *Service) Get(
	ctx context.Context,
	principal auth.Principal,
	requestID string,
) (*job.Job, error) {
	currentJob, err := s.jobs.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if currentJob.Kind != job.KindProfiling {
		return nil, job.ErrNotFound
	}
	if !principal.IsAdmin && currentJob.UserID != principal.ID {
		return nil, auth.ErrPermissionDenied
	}
	return currentJob, nil
}

// List returns one authorized Profiling Job page.
func (s *Service) List(
	ctx context.Context,
	principal auth.Principal,
	limit int,
	offset int,
) (*job.Page, error) {
	return s.jobs.ListPage(ctx, &job.Query{
		UserID:  principal.ID,
		IsAdmin: principal.IsAdmin,
		Kinds:   []job.Kind{job.KindProfiling},
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

// RawProfiles returns an authorized page of published result records.
func (s *Service) RawProfiles(
	ctx context.Context,
	principal auth.Principal,
	requestID string,
	limit int,
	offset int,
) (*RawProfilePage, error) {
	currentJob, err := s.Get(ctx, principal, requestID)
	if err != nil {
		return nil, err
	}
	switch currentJob.Status {
	case job.StatusPending, job.StatusRunning, job.StatusStopping:
		return nil, ErrResultNotReady
	case job.StatusTerminal:
		if currentJob.Terminal == nil {
			return nil, ErrResultUnavailable
		}
		switch currentJob.Terminal.Outcome {
		case job.OutcomeStopped, job.OutcomeFailed:
			return nil, ErrResultUnavailable
		case job.OutcomeCompleted, job.OutcomeUnknown:
		default:
			return nil, fmt.Errorf("unsupported Job outcome %q", currentJob.Terminal.Outcome)
		}
	default:
		return nil, fmt.Errorf("unsupported Job status %q", currentJob.Status)
	}
	if s.profileStore == nil || s.publications == nil {
		return nil, ErrResultUnavailable
	}
	published, err := s.isPublished(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if !published {
		return nil, fmt.Errorf(
			"%w: profiling result %q has no publication marker",
			ErrResultNotFound,
			requestID,
		)
	}

	documents, err := s.profileStore.ListByTracerID(ctx, requestID, limit+1, offset)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: query profiles for request %q: %w",
			ErrResultStoreUnavailable,
			requestID,
			err,
		)
	}
	hasMore := len(documents) > limit
	if hasMore {
		documents = documents[:limit]
	}
	items := make([]*RawProfile, 0, len(documents))
	for _, document := range documents {
		items = append(items, &RawProfile{
			Hostname:          document.Hostname,
			Region:            document.Region,
			UploadedTimestamp: document.UploadedTimestamp,
			StartedTimestamp:  *document.StartedTimestamp,
			ContainerID:       document.ContainerID,
			ContainerHostname: document.ContainerHostname,
			ContainerType:     document.ContainerType,
			ContainerQoS:      document.ContainerQoS,
			ProfileType:       document.ProfileData.ProfileType,
			Profile:           document.ProfileData.Profile,
		})
	}
	return &RawProfilePage{
		Items:   items,
		Limit:   limit,
		Offset:  offset,
		HasMore: hasMore,
	}, nil
}

// Capabilities returns the versioned static Profiling capability table.
func (*Service) Capabilities() []profilingdomain.Capability {
	return profilingdomain.Capabilities()
}

// ResultURL returns a Job-scoped dashboard URL only for published results.
func (s *Service) ResultURL(
	ctx context.Context,
	resultJob *job.Job,
) (*string, error) {
	if s.dashboardBaseURL == "" || resultJob == nil || resultJob.EndedAt.IsZero() {
		return nil, nil
	}
	if resultJob.Status != job.StatusTerminal || resultJob.Terminal == nil ||
		(resultJob.Terminal.Outcome != job.OutcomeCompleted &&
			resultJob.Terminal.Outcome != job.OutcomeUnknown) {
		return nil, nil
	}
	if s.publications == nil {
		return nil, nil
	}
	published, err := s.isPublished(ctx, resultJob.ID)
	if err != nil {
		return nil, err
	}
	if !published {
		return nil, nil
	}
	return buildDashboardURL(s.dashboardBaseURL, resultJob), nil
}

func (s *Service) isPublished(ctx context.Context, requestID string) (bool, error) {
	published, err := s.publications.IsPublished(ctx, requestID)
	if err != nil {
		return false, fmt.Errorf(
			"%w: query publication for request %q: %w",
			ErrResultStoreUnavailable,
			requestID,
			err,
		)
	}
	return published, nil
}

func buildDashboardURL(baseURL string, resultJob *job.Job) *string {
	if baseURL == "" || resultJob == nil || resultJob.Spec.Profiling == nil {
		return nil
	}

	var dashboardUID, dashboardSlug, scopeKey, scopeValue string
	switch resultJob.Scope {
	case observation.ScopeContainer:
		scopeKey = "var-container_id"
		scopeValue = resultJob.ContainerID
		switch resultJob.Spec.Profiling.Type {
		case profilingdomain.TypeMemory:
			dashboardUID = "container-memory-profiling"
			dashboardSlug = "e5aeb9-e599a8-memory-profiling"
		case profilingdomain.TypeCPU:
			dashboardUID = "container-cpu-profiling"
			dashboardSlug = "e5aeb9-e599a8-cpu-profiling"
		}
	case observation.ScopeHost:
		scopeKey = "var-hostname"
		scopeValue = resultJob.Hostname
		switch resultJob.Spec.Profiling.Type {
		case profilingdomain.TypeMemory:
			dashboardUID = "host-memory-profiling"
			dashboardSlug = "e5aebf-e4b8bb-e69cba-memory-profiling"
		case profilingdomain.TypeCPU:
			dashboardUID = "host-cpu-profiling"
			dashboardSlug = "e5aebf-e4b8bb-e69cba-cpu-profiling"
		}
	default:
		return nil
	}
	if dashboardUID == "" {
		return nil
	}
	query := url.Values{}
	query.Set("orgId", "1")
	query.Set("from", resultJob.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"))
	query.Set("to", resultJob.EndedAt.UTC().Format("2006-01-02T15:04:05.000Z"))
	query.Set("timezone", "browser")
	query.Set(scopeKey, scopeValue)
	query.Set("var-tracer_id", resultJob.ID)
	result := fmt.Sprintf(
		"%s/%s/%s?%s",
		strings.TrimRight(baseURL, "/"),
		dashboardUID,
		dashboardSlug,
		query.Encode(),
	)
	return &result
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

// NormalizeRawProfilePage applies the raw Profile pagination defaults.
func NormalizeRawProfilePage(limit, offset *int) (int, int) {
	normalizedLimit := defaultRawProfilePageSize
	if limit != nil {
		normalizedLimit = *limit
	}
	normalizedOffset := 0
	if offset != nil {
		normalizedOffset = *offset
	}
	return normalizedLimit, normalizedOffset
}

func validateCreateInput(input *CreateInput) error {
	if input == nil {
		return errors.New("profiling input is required")
	}
	if input.Hostname == "" || strings.TrimSpace(input.Hostname) != input.Hostname {
		return errors.New("hostname must be a non-empty trimmed value")
	}
	if input.DurationSeconds > math.MaxInt64/int64(time.Second) {
		return errors.New("duration_seconds is outside the supported range")
	}
	return nil
}
