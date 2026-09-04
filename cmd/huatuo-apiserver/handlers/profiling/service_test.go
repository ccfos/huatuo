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
	"net/url"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/auth"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"
)

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

func TestResultURLRequiresPublishedCompleteResult(t *testing.T) {
	service, err := NewService(
		&job.Manager{},
		nil,
		nil,
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
		Status:    job.StatusTerminal,
		Terminal:  &job.TerminalResult{Outcome: job.OutcomeCompleted},
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
	if resultURL != nil {
		t.Fatalf("ResultURL() = %q without publication store, want nil", *resultURL)
	}

	failed := *completed
	failed.Status = job.StatusTerminal
	failed.Terminal = &job.TerminalResult{Outcome: job.OutcomeFailed,
		Reason: job.FailureReasonExecutionFailed, Message: "failed"}
	if resultURL, err := service.ResultURL(t.Context(), &failed); err != nil || resultURL != nil {
		t.Fatalf("failed ResultURL() = (%v, %v)", resultURL, err)
	}
}

func TestBuildDashboardURL(t *testing.T) {
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	newJob := func(scope observation.Scope, profileType profilingdomain.Type) *job.Job {
		return &job.Job{
			ID:          "job-1",
			Hostname:    "node+1&debug",
			Scope:       scope,
			ContainerID: "container+1&debug",
			CreatedAt:   base,
			EndedAt:     base.Add(time.Minute),
			Spec: job.Spec{Profiling: &profilingdomain.Spec{
				Type: profileType,
			}},
		}
	}
	tests := []struct {
		name           string
		baseURL        string
		input          *job.Job
		wantPath       string
		wantScopeKey   string
		wantScopeValue string
	}{
		{
			name:           "host cpu",
			baseURL:        "https://grafana.example/d/",
			input:          newJob(observation.ScopeHost, profilingdomain.TypeCPU),
			wantPath:       "/d/host-cpu-profiling/e5aebf-e4b8bb-e69cba-cpu-profiling",
			wantScopeKey:   "var-hostname",
			wantScopeValue: "node+1&debug",
		},
		{
			name:           "container memory",
			baseURL:        "https://grafana.example/d/",
			input:          newJob(observation.ScopeContainer, profilingdomain.TypeMemory),
			wantPath:       "/d/container-memory-profiling/e5aeb9-e599a8-memory-profiling",
			wantScopeKey:   "var-container_id",
			wantScopeValue: "container+1&debug",
		},
		{
			name:    "missing base url",
			input:   newJob(observation.ScopeHost, profilingdomain.TypeCPU),
			baseURL: "",
		},
		{
			name:    "missing job",
			baseURL: "https://grafana.example/d",
		},
		{
			name:    "missing profiling spec",
			baseURL: "https://grafana.example/d",
			input:   &job.Job{},
		},
		{
			name:    "unsupported scope",
			baseURL: "https://grafana.example/d",
			input:   newJob(observation.ScopeUnknown, profilingdomain.TypeCPU),
		},
		{
			name:    "unsupported profile type",
			baseURL: "https://grafana.example/d",
			input:   newJob(observation.ScopeHost, profilingdomain.TypeLock),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildDashboardURL(tt.baseURL, tt.input)
			if tt.wantPath == "" {
				if got != nil {
					t.Fatalf("buildDashboardURL() = %q, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("buildDashboardURL() = nil, want URL")
			}
			parsed, err := url.Parse(*got)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", *got, err)
			}
			if parsed.Path != tt.wantPath {
				t.Fatalf("buildDashboardURL() path = %q, want %q", parsed.Path, tt.wantPath)
			}
			query := parsed.Query()
			if got := query.Get(tt.wantScopeKey); got != tt.wantScopeValue {
				t.Errorf("buildDashboardURL() scope = %q, want %q", got, tt.wantScopeValue)
			}
			if got := query.Get("var-tracer_id"); got != "job-1" {
				t.Errorf("buildDashboardURL() tracer ID = %q, want %q", got, "job-1")
			}
			if got := query.Get("from"); got != "2026-08-24T12:00:00.000Z" {
				t.Errorf("buildDashboardURL() from = %q", got)
			}
			if got := query.Get("to"); got != "2026-08-24T12:01:00.000Z" {
				t.Errorf("buildDashboardURL() to = %q", got)
			}
		})
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
	_, err := NewService(nil, nil, nil, Config{})
	if err == nil || !strings.Contains(err.Error(), "job manager is required") {
		t.Fatalf("NewService() error = %v", err)
	}
}
