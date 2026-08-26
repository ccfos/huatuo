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
	"testing"

	"huatuo-bamai/internal/job"
)

type stubJobReader struct {
	job *job.Job
	err error
}

func (s *stubJobReader) Get(context.Context, string) (*job.Job, error) {
	return s.job, s.err
}

type stubRepository struct {
	published bool
	profiles  []Profile
	listLimit int
	listCalls int
}

func (s *stubRepository) IsPublished(context.Context, string) (bool, error) {
	return s.published, nil
}

func (s *stubRepository) List(
	_ context.Context,
	_ string,
	limit int,
	_ int,
) ([]Profile, error) {
	s.listLimit = limit
	s.listCalls++
	return append([]Profile(nil), s.profiles...), nil
}

func TestServiceAllowsResultsOnlyForCertainTerminalStates(t *testing.T) {
	tests := []struct {
		name      string
		status    job.Status
		published bool
		wantErr   error
		wantList  bool
	}{
		{
			name:      "completed with publication",
			status:    job.StatusCompleted,
			published: true,
			wantList:  true,
		},
		{
			name:    "completed without publication",
			status:  job.StatusCompleted,
			wantErr: ErrNotFound,
		},
		{
			name:      "outcome unknown with publication",
			status:    job.StatusOutcomeUnknown,
			published: true,
			wantList:  true,
		},
		{
			name:    "outcome unknown without publication",
			status:  job.StatusOutcomeUnknown,
			wantErr: ErrNotFound,
		},
		{
			name:    "failed",
			status:  job.StatusFailed,
			wantErr: ErrUnavailable,
		},
		{
			name:    "stopped",
			status:  job.StatusStopped,
			wantErr: ErrUnavailable,
		},
		{
			name:    "running",
			status:  job.StatusRunning,
			wantErr: ErrNotReady,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository := &stubRepository{
				published: tt.published,
				profiles:  []Profile{{Hostname: "node-1"}},
			}
			service, err := NewService(&stubJobReader{job: &job.Job{
				ID:     "job-1",
				Kind:   job.KindProfiling,
				UserID: "user-1",
				Status: tt.status,
			}}, repository)
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			page, err := service.List(t.Context(), "job-1", "user-1", false, 10, 0)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("List() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantList && (page == nil || len(page.Items) != 1) {
				t.Fatalf("List() page = %+v", page)
			}
			if !tt.wantList && repository.listCalls != 0 {
				t.Fatalf("repository List() calls = %d, want 0", repository.listCalls)
			}
		})
	}
}

func TestServiceEnforcesOwnershipAndKind(t *testing.T) {
	tests := []struct {
		name    string
		kind    job.Kind
		userID  string
		isAdmin bool
		wantErr error
	}{
		{
			name:    "other user",
			kind:    job.KindProfiling,
			userID:  "user-2",
			wantErr: ErrForbidden,
		},
		{
			name:    "wrong kind",
			kind:    job.KindTracing,
			userID:  "user-1",
			wantErr: ErrWrongKind,
		},
		{
			name:    "admin",
			kind:    job.KindProfiling,
			userID:  "user-2",
			isAdmin: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, err := NewService(&stubJobReader{job: &job.Job{
				ID:     "job-1",
				Kind:   tt.kind,
				UserID: "user-1",
				Status: job.StatusCompleted,
			}}, &stubRepository{published: true})
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			_, err = service.List(t.Context(), "job-1", tt.userID, tt.isAdmin, 10, 0)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("List() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestServiceFetchesOneExtraProfileForHasMore(t *testing.T) {
	profiles := make([]Profile, maxRawProfilePageSize+1)
	repository := &stubRepository{published: true, profiles: profiles}
	service, err := NewService(&stubJobReader{job: &job.Job{
		ID:     "job-1",
		Kind:   job.KindProfiling,
		UserID: "user-1",
		Status: job.StatusCompleted,
	}}, repository)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	page, err := service.List(
		t.Context(),
		"job-1",
		"user-1",
		false,
		maxRawProfilePageSize,
		0,
	)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if repository.listLimit != maxRawProfilePageSize+1 {
		t.Fatalf(
			"repository limit = %d, want %d",
			repository.listLimit,
			maxRawProfilePageSize+1,
		)
	}
	if len(page.Items) != maxRawProfilePageSize || !page.HasMore {
		t.Fatalf("page = (items=%d, has_more=%t)", len(page.Items), page.HasMore)
	}
}
