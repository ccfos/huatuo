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
	profileservice "huatuo-bamai/internal/profiler/service"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

const (
	defaultPageSize           = 100
	maxPageSize               = 1000
	defaultRawProfilePageSize = 20
	maxRawProfilePageSize     = 100
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
	UploadedAt        time.Time
	CapturedAt        time.Time
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

// RawProfileReader reads stored profiling windows by task identifier.
type RawProfileReader interface {
	ListByTracerID(
		ctx context.Context,
		tracerID string,
		limit int,
		offset int,
	) ([]*profileservice.ProfileDocument, error)
}

// PublicationReader checks the durable result commit marker.
type PublicationReader interface {
	IsPublished(ctx context.Context, requestID string) (bool, error)
}

// Service owns Profiling authorization, validation, Job commands, and result access.
type Service struct {
	jobs             *job.Manager
	profiles         RawProfileReader
	publications     PublicationReader
	dashboardBaseURL string
}

// NewService constructs the Profiling application service.
func NewService(
	jobs *job.Manager,
	profiles RawProfileReader,
	publications PublicationReader,
	config Config,
) (*Service, error) {
	if jobs == nil {
		return nil, errors.New("create profiling service: job manager is required")
	}
	if (profiles == nil) != (publications == nil) {
		return nil, errors.New(
			"create profiling service: profile storage and publication store must be configured together",
		)
	}
	return &Service{
		jobs:             jobs,
		profiles:         profiles,
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
	if err := validateCreateInput(principal, input); err != nil {
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
	jobEntity, err := s.jobs.Get(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if jobEntity.Kind != job.KindProfiling {
		return nil, job.ErrNotFound
	}
	if !principal.IsAdmin && jobEntity.UserID != principal.ID {
		return nil, auth.ErrPermissionDenied
	}
	return jobEntity, nil
}

// List returns one authorized Profiling Job page.
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
	if requestID == "" {
		return nil, fmt.Errorf("%w: request ID is required", job.ErrInvalidQuery)
	}
	if err := validateRawProfilePage(limit, offset); err != nil {
		return nil, err
	}
	jobEntity, err := s.Get(ctx, principal, requestID)
	if err != nil {
		return nil, err
	}
	switch jobEntity.Status {
	case job.StatusPending, job.StatusRunning, job.StatusStopping:
		return nil, ErrResultNotReady
	case job.StatusStopped, job.StatusFailed:
		return nil, ErrResultUnavailable
	case job.StatusCompleted, job.StatusOutcomeUnknown:
	default:
		return nil, fmt.Errorf("unsupported Job status %q", jobEntity.Status)
	}
	if s.profiles == nil || s.publications == nil {
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

	documents, err := s.profiles.ListByTracerID(ctx, requestID, limit+1, offset)
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
		if document == nil {
			return nil, fmt.Errorf(
				"%w: profile storage returned a nil document",
				ErrResultStoreUnavailable,
			)
		}
		items = append(items, &RawProfile{
			Hostname:          document.Hostname,
			Region:            document.Region,
			UploadedAt:        document.UploadedTime,
			CapturedAt:        document.CapturedAt(),
			ContainerID:       document.ContainerID,
			ContainerHostname: document.ContainerHostname,
			ContainerType:     document.ContainerType,
			ContainerQoS:      document.ContainerQOS,
			ProfileType:       document.TracerData.Flamedata.ProfileType,
			Profile:           &document.TracerData.Flamedata.Profile,
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
	jobEntity *job.Job,
) (*string, error) {
	if s.dashboardBaseURL == "" || jobEntity == nil || jobEntity.EndedAt.IsZero() {
		return nil, nil
	}
	switch jobEntity.Status {
	case job.StatusCompleted, job.StatusOutcomeUnknown:
	default:
		return nil, nil
	}
	if s.publications == nil {
		return nil, nil
	}
	published, err := s.isPublished(ctx, jobEntity.ID)
	if err != nil {
		return nil, err
	}
	if !published {
		return nil, nil
	}
	return buildDashboardURL(s.dashboardBaseURL, jobEntity), nil
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

func buildDashboardURL(baseURL string, jobEntity *job.Job) *string {
	if baseURL == "" || jobEntity == nil || jobEntity.Spec.Profiling == nil {
		return nil
	}

	var dashboardUID, dashboardSlug, scopeKey, scopeValue string
	switch jobEntity.Scope {
	case observation.ScopeContainer:
		scopeKey = "var-container_id"
		scopeValue = jobEntity.ContainerID
		switch jobEntity.Spec.Profiling.Type {
		case profilingdomain.TypeMemory:
			dashboardUID = "container-memory-profiling"
			dashboardSlug = "e5aeb9-e599a8-memory-profiling"
		case profilingdomain.TypeCPU:
			dashboardUID = "container-cpu-profiling"
			dashboardSlug = "e5aeb9-e599a8-cpu-profiling"
		}
	case observation.ScopeHost:
		scopeKey = "var-hostname"
		scopeValue = jobEntity.Hostname
		switch jobEntity.Spec.Profiling.Type {
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
	query.Set("from", jobEntity.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"))
	query.Set("to", jobEntity.EndedAt.UTC().Format("2006-01-02T15:04:05.000Z"))
	query.Set("timezone", "browser")
	query.Set(scopeKey, scopeValue)
	query.Set("var-tracer_id", jobEntity.ID)
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

func validateCreateInput(principal auth.Principal, input *CreateInput) error {
	if principal.ID == "" {
		return errors.New("authenticated user ID is required")
	}
	if input == nil {
		return errors.New("profiling input is required")
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
	if !profilingdomain.SupportsScope(input.Spec.Language, input.Spec.Type, input.Scope) {
		return fmt.Errorf("profiling combination does not support scope %q", input.Scope)
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

func validateRawProfilePage(limit, offset int) error {
	if limit <= 0 || limit > maxRawProfilePageSize {
		return fmt.Errorf(
			"%w: limit must be between 1 and %d",
			job.ErrInvalidQuery,
			maxRawProfilePageSize,
		)
	}
	if offset < 0 {
		return fmt.Errorf("%w: offset must not be negative", job.ErrInvalidQuery)
	}
	return nil
}
