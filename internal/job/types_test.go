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
	"strings"
	"testing"
	"time"

	"huatuo-bamai/pkg/observation"
	"huatuo-bamai/pkg/profiling"
)

func TestJobValidateRequiresFailureOnlyForFailedStatus(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		mutate  func(*Job)
		wantErr string
	}{
		{
			name: "completed without failure",
		},
		{
			name: "failed without reason",
			mutate: func(jobEntity *Job) {
				jobEntity.Status = StatusFailed
			},
			wantErr: "failure must be present",
		},
		{
			name: "outcome unknown with failure",
			mutate: func(jobEntity *Job) {
				jobEntity.Status = StatusOutcomeUnknown
				jobEntity.Failure = &TerminalFailure{
					Reason:  FailureReasonOperationLost,
					Message: "lost",
				}
			},
			wantErr: "failure must be present exactly when status is failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobEntity := &Job{
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
				Status:    StatusCompleted,
				CreatedAt: now,
				UpdatedAt: now,
				EndedAt:   now,
			}
			if tt.mutate != nil {
				tt.mutate(jobEntity)
			}
			err := jobEntity.validate()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validate() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCloneJobDoesNotAliasNestedState(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	source := testJob("job-1", StatusFailed, now)
	source.Failure = &TerminalFailure{
		Reason:  FailureReasonExecutionFailed,
		Message: "failed",
	}
	cloned := cloneJob(source)
	cloned.Spec.Profiling.Mode = profiling.ModeOffCPU
	cloned.Failure.Message = "changed"

	if source.Spec.Profiling.Mode != profiling.ModeOnCPU {
		t.Fatalf("source profiling mode = %q", source.Spec.Profiling.Mode)
	}
	if source.Failure.Message != "failed" {
		t.Fatalf("source failure message = %q", source.Failure.Message)
	}
}

func TestJobValidateRequiresEndedAtExactlyForTerminalStatus(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	terminal := testJob("terminal", StatusCompleted, now)
	terminal.EndedAt = time.Time{}
	if err := terminal.validate(); err == nil || !strings.Contains(err.Error(), "ended timestamp") {
		t.Fatalf("terminal validate() error = %v", err)
	}

	nonTerminal := testJob("running", StatusRunning, now)
	nonTerminal.StartedAt = now
	nonTerminal.ExecutionDeadline = now.Add(time.Minute)
	nonTerminal.EndedAt = now
	if err := nonTerminal.validate(); err == nil || !strings.Contains(err.Error(), "ended timestamp") {
		t.Fatalf("non-terminal validate() error = %v", err)
	}
}
