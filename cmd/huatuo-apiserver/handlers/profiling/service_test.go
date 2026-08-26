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
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/auth"
	"huatuo-bamai/internal/job"
	profilingresult "huatuo-bamai/internal/profiling/result"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"
)

type resultJobReader struct{}

func (resultJobReader) Get(context.Context, string) (*job.Job, error) {
	return nil, errors.New("unexpected Job read")
}

type publishedResultRepository struct{}

func (publishedResultRepository) IsPublished(context.Context, string) (bool, error) {
	return true, nil
}

func (publishedResultRepository) List(
	context.Context,
	string,
	int,
	int,
) ([]profilingresult.Profile, error) {
	return nil, errors.New("unexpected result list")
}

func TestValidateCreateInput(t *testing.T) {
	valid := CreateInput{
		Hostname:        "node-1",
		DurationSeconds: 60,
		Scope:           observation.ScopeHost,
		Spec: profilingdomain.Spec{
			Type:     profilingdomain.TypeCPU,
			Language: profilingdomain.LanguageGo,
			Mode:     profilingdomain.ModeOnCPU,
		},
	}
	tests := []struct {
		name      string
		principal auth.Principal
		mutate    func(*CreateInput)
		wantErr   string
	}{
		{name: "valid", principal: auth.Principal{ID: "user-1"}},
		{name: "missing principal", wantErr: "authenticated user ID"},
		{
			name:      "untrimmed hostname",
			principal: auth.Principal{ID: "user-1"},
			mutate: func(input *CreateInput) {
				input.Hostname = " node-1"
			},
			wantErr: "hostname",
		},
		{
			name:      "container ID on host scope",
			principal: auth.Principal{ID: "user-1"},
			mutate: func(input *CreateInput) {
				input.ContainerID = "container-1"
			},
			wantErr: "container id must be empty",
		},
		{
			name:      "unsupported language and type",
			principal: auth.Principal{ID: "user-1"},
			mutate: func(input *CreateInput) {
				input.Spec.Language = profilingdomain.LanguagePython
				input.Spec.Type = profilingdomain.TypeMemory
				input.Spec.Mode = profilingdomain.ModeObjectAlloc
			},
			wantErr: "not supported",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := valid
			if tt.mutate != nil {
				tt.mutate(&input)
			}
			err := validateCreateInput(tt.principal, &input)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validateCreateInput() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("validateCreateInput() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestResultURLIsAvailableOnlyForCompleteResults(t *testing.T) {
	results, err := profilingresult.NewService(resultJobReader{}, publishedResultRepository{})
	if err != nil {
		t.Fatalf("NewService(result) error = %v", err)
	}
	service, err := NewService(
		&job.Manager{},
		results,
		Config{DashboardBaseURL: "https://grafana.example/d"},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	completed := &job.Job{
		ID:        "job-1",
		Kind:      job.KindProfiling,
		Hostname:  "node+1&debug",
		Scope:     observation.ScopeHost,
		Status:    job.StatusCompleted,
		CreatedAt: base,
		EndedAt:   base.Add(time.Minute),
		Spec: job.Spec{Profiling: &profilingdomain.Spec{
			Type: profilingdomain.TypeCPU,
		}},
	}

	resultURL, err := service.ResultURL(t.Context(), completed)
	if err != nil {
		t.Fatalf("ResultURL() error = %v", err)
	}
	if resultURL == nil || !strings.Contains(*resultURL, "var-hostname=node%2B1%26debug") ||
		!strings.Contains(*resultURL, "var-tracer_id=job-1") {
		t.Fatalf("ResultURL() = %v", resultURL)
	}

	failed := *completed
	failed.Status = job.StatusFailed
	if resultURL, err := service.ResultURL(t.Context(), &failed); err != nil || resultURL != nil {
		t.Fatalf("failed ResultURL() = (%v, %v)", resultURL, err)
	}
}

func TestNormalizePage(t *testing.T) {
	limit, offset := NormalizePage(nil, nil)
	if limit != 100 || offset != 0 {
		t.Fatalf("NormalizePage(nil, nil) = (%d, %d)", limit, offset)
	}
	customLimit, customOffset := 25, 50
	limit, offset = NormalizePage(&customLimit, &customOffset)
	if limit != customLimit || offset != customOffset {
		t.Fatalf("NormalizePage(custom) = (%d, %d)", limit, offset)
	}
}

func TestNewServiceRequiresJobManager(t *testing.T) {
	_, err := NewService(nil, nil, Config{})
	if err == nil || !strings.Contains(err.Error(), "Job Manager is required") {
		t.Fatalf("NewService() error = %v", err)
	}
}
