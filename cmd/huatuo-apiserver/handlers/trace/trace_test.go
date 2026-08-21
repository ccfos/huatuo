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
	"testing"
	"time"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/job"
)

func TestValidateCreateTraceJobRequest(t *testing.T) {
	tests := []struct {
		name    string
		request v1.CreateTraceJobRequest
		wantErr bool
	}{
		{name: "valid", request: v1.CreateTraceJobRequest{Type: "tracing", DurationSeconds: 30, Hostname: "node-a"}},
		{name: "missing hostname", request: v1.CreateTraceJobRequest{Type: "tracing", DurationSeconds: 30}, wantErr: true},
		{name: "zero duration", request: v1.CreateTraceJobRequest{Type: "tracing", Hostname: "node-a"}, wantErr: true},
		{name: "duration too long", request: v1.CreateTraceJobRequest{Type: "tracing", DurationSeconds: 301, Hostname: "node-a"}, wantErr: true},
		{name: "missing type", request: v1.CreateTraceJobRequest{DurationSeconds: 30, Hostname: "node-a"}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCreateTraceJobRequest(&test.request)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateCreateTraceJobRequest() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestBuildTraceJob(t *testing.T) {
	start := time.Date(2026, 7, 21, 6, 0, 0, 123, time.UTC)
	finished := start.Add(30 * time.Second)
	traceJob := buildTraceJob(&job.Job{
		ID:           "job-2026",
		Status:       job.JobStatusFailed,
		Duration:     30,
		CreatedAt:    start,
		FinishedAt:   finished,
		ErrorMessage: "agent failed",
		Result:       job.Result{URL: "https://trace.example/job-2026"},
		AgentTask:    job.AgentTaskRequest{TracerName: "tracer"},
	})

	if !traceJob.CreatedAt.Equal(start) {
		t.Errorf("CreatedAt = %s, want %s", traceJob.CreatedAt, start)
	}
	if traceJob.FinishedAt == nil || !traceJob.FinishedAt.Equal(finished) {
		t.Errorf("FinishedAt = %v, want %s", traceJob.FinishedAt, finished)
	}
	if traceJob.StatusReason == nil || *traceJob.StatusReason != "agent failed" {
		t.Errorf("StatusReason = %v, want agent failed", traceJob.StatusReason)
	}
	if traceJob.ResultURL == nil || *traceJob.ResultURL != "https://trace.example/job-2026" {
		t.Errorf("ResultURL = %v, want trace result URL", traceJob.ResultURL)
	}
	if traceJob.DurationSeconds != 30 {
		t.Errorf("DurationSeconds = %d, want 30", traceJob.DurationSeconds)
	}
	if traceJob.Type != "tracing" {
		t.Errorf("Type = %q, want tracing", traceJob.Type)
	}
}

func TestBuildTraceJobOmitsUnavailableTerminalFields(t *testing.T) {
	traceJob := buildTraceJob(&job.Job{})

	if traceJob.FinishedAt != nil {
		t.Errorf("FinishedAt=%v, want nil", traceJob.FinishedAt)
	}
	if traceJob.ResultURL != nil {
		t.Errorf("ResultURL=%v, want nil", traceJob.ResultURL)
	}
	if traceJob.StatusReason != nil {
		t.Errorf("StatusReason=%v, want nil", traceJob.StatusReason)
	}
}
