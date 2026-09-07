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

package job

import (
	"errors"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/pkg/observation"
	"huatuo-bamai/pkg/profiling"
)

func TestValidateManagerConfigRequiresBothServicePolicies(t *testing.T) {
	err := validateManagerConfig(&ManagerConfig{
		StoreDSN:        "jobs.db",
		ProfilingPolicy: Policy{MaxJobsPerHost: 1, MaxTotalJobs: 1},
	})
	if err == nil || err.Error() != "create job manager: policy for tracing is required" {
		t.Fatalf("validateManagerConfig() error = %v", err)
	}
}

func TestValidateManagerConfigRequiresLifecyclePolicy(t *testing.T) {
	err := validateManagerConfig(&ManagerConfig{
		StoreDSN:                   "jobs.db",
		ProfilingPolicy:            Policy{MaxJobsPerHost: 1, MaxTotalJobs: 1},
		TracingPolicy:              Policy{MaxJobsPerHost: 1, MaxTotalJobs: 1},
		StatusPollInterval:         time.Second,
		CompletionGracePeriod:      time.Second,
		NodeUnavailableGracePeriod: time.Second,
		JobRetentionPeriod:         time.Second,
	})
	if err == nil || err.Error() != "create job manager: pending timeout must be positive" {
		t.Fatalf("validateManagerConfig() error = %v", err)
	}
}

func TestValidateManagerConfigRequiresStoreDSN(t *testing.T) {
	err := validateManagerConfig(&ManagerConfig{})
	if err == nil || err.Error() != "create job manager: store dsn is required" {
		t.Fatalf("validateManagerConfig() error = %v", err)
	}
}

func TestValidateListPageQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   *Query
		wantErr bool
	}{
		{name: "valid", query: &Query{Limit: 1}},
		{name: "nil", wantErr: true},
		{name: "zero limit", query: &Query{}, wantErr: true},
		{name: "limit too large", query: &Query{Limit: maxJobPageSize + 1}, wantErr: true},
		{name: "negative offset", query: &Query{Limit: 1, Offset: -1}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateListPageQuery(tt.query)
			if !tt.wantErr && err != nil {
				t.Fatalf("validateListPageQuery() error = %v", err)
			}
			if tt.wantErr && !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("validateListPageQuery() error = %v, want ErrInvalidQuery", err)
			}
		})
	}
}

func TestCreateRequestValidate(t *testing.T) {
	validRequest := func() *CreateRequest {
		return &CreateRequest{
			UserID:   "user-1",
			Hostname: "node-1",
			Duration: time.Minute,
			Scope:    observation.ScopeHost,
			Spec: Spec{Profiling: &profiling.Spec{
				Type:     profiling.TypeCPU,
				Language: profiling.LanguageGo,
				Mode:     profiling.ModeOnCPU,
			}},
		}
	}
	tests := []struct {
		name    string
		input   func() *CreateRequest
		wantErr string
	}{
		{name: "valid", input: validRequest},
		{name: "nil", wantErr: "request is required"},
		{
			name: "missing user ID",
			input: func() *CreateRequest {
				request := validRequest()
				request.UserID = ""
				return request
			},
			wantErr: "user ID",
		},
		{
			name: "missing hostname",
			input: func() *CreateRequest {
				request := validRequest()
				request.Hostname = ""
				return request
			},
			wantErr: "hostname",
		},
		{
			name: "invalid duration",
			input: func() *CreateRequest {
				request := validRequest()
				request.Duration = time.Millisecond
				return request
			},
			wantErr: "duration",
		},
		{
			name: "invalid scope",
			input: func() *CreateRequest {
				request := validRequest()
				request.Scope = observation.ScopeUnknown
				return request
			},
			wantErr: "scope",
		},
		{
			name: "missing spec",
			input: func() *CreateRequest {
				request := validRequest()
				request.Spec = Spec{}
				return request
			},
			wantErr: "kind",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var input *CreateRequest
			if tt.input != nil {
				input = tt.input()
			}
			err := input.validate()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validate() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestJobValidateStateRequiresTerminalResultForTerminalStatus(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		mutate  func(*Job)
		wantErr string
	}{
		{name: "completed without failure"},
		{name: "terminal without result", mutate: func(job *Job) { job.Terminal = nil }, wantErr: "terminal result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &Job{
				ID:       "job-1",
				Kind:     KindProfiling,
				UserID:   "user-1",
				Hostname: "node-1",
				Duration: time.Minute,
				Scope:    observation.ScopeHost,
				Spec: Spec{Profiling: &profiling.Spec{
					Type:     profiling.TypeCPU,
					Language: profiling.LanguageGo,
					Mode:     profiling.ModeOnCPU,
				}},
				Status:    StatusTerminal,
				Terminal:  &TerminalResult{Outcome: OutcomeCompleted},
				CreatedAt: now,
				UpdatedAt: now,
				EndedAt:   now,
			}
			if tt.mutate != nil {
				tt.mutate(input)
			}
			err := input.validateState()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validateState() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("validateState() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestJobValidateStateRequiresEndedAtExactlyForTerminalStatus(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	terminal := testJob("terminal", StatusTerminal, now)
	terminal.EndedAt = time.Time{}
	if err := terminal.validateState(); err == nil || !strings.Contains(err.Error(), "ended timestamp") {
		t.Fatalf("terminal validateState() error = %v", err)
	}

	nonTerminal := testJob("running", StatusRunning, now)
	nonTerminal.StartedAt = now
	nonTerminal.ExecutionDeadline = now.Add(time.Minute)
	nonTerminal.EndedAt = now
	if err := nonTerminal.validateState(); err == nil || !strings.Contains(err.Error(), "ended timestamp") {
		t.Fatalf("non-terminal validateState() error = %v", err)
	}
}

func TestInvalidNodeRequestIsValidFailureReason(t *testing.T) {
	if !isValidFailureReason(FailureReasonInvalidNodeRequest) {
		t.Fatal("invalid_node_request must be a valid Job failure reason")
	}
}

func TestJobValidateStoredRequiresPersistentFields(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		mutate  func(*Job)
		wantErr string
	}{
		{name: "valid"},
		{name: "missing ID", mutate: func(job *Job) { job.ID = "" }, wantErr: "ID"},
		{
			name:    "missing created timestamp",
			mutate:  func(job *Job) { job.CreatedAt = time.Time{} },
			wantErr: "created and updated timestamps",
		},
		{
			name:    "missing updated timestamp",
			mutate:  func(job *Job) { job.UpdatedAt = time.Time{} },
			wantErr: "created and updated timestamps",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := testJob("job-1", StatusPending, now)
			if tt.mutate != nil {
				tt.mutate(input)
			}
			err := input.validateStored()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validateStored() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("validateStored() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
