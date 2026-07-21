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
		{name: "valid", request: v1.CreateTraceJobRequest{Type: "tracing", Duration: 30, Hostname: "node-a"}},
		{name: "missing hostname", request: v1.CreateTraceJobRequest{Type: "tracing", Duration: 30}, wantErr: true},
		{name: "zero duration", request: v1.CreateTraceJobRequest{Type: "tracing", Hostname: "node-a"}, wantErr: true},
		{name: "duration too long", request: v1.CreateTraceJobRequest{Type: "tracing", Duration: 301, Hostname: "node-a"}, wantErr: true},
		{name: "missing type", request: v1.CreateTraceJobRequest{Duration: 30, Hostname: "node-a"}, wantErr: true},
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

func TestConvertJobToTraceResponse(t *testing.T) {
	start := time.Date(2026, 7, 21, 6, 0, 0, 123, time.UTC)
	response := convertJobToTraceResponse(&job.Job{
		ID:           "job-2026",
		Status:       job.JobStatusFailed,
		StartTime:    start,
		ErrorMessage: "agent failed",
	})

	if response.StartTime != start.Format(time.RFC3339Nano) {
		t.Errorf("StartTime = %q, want %q", response.StartTime, start.Format(time.RFC3339Nano))
	}
	if response.EndTime != "" {
		t.Errorf("EndTime = %q, want empty", response.EndTime)
	}
	if response.ErrorMessage != "agent failed" {
		t.Errorf("ErrorMessage = %q, want %q", response.ErrorMessage, "agent failed")
	}
}
