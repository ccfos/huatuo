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

package handlers

import (
	"errors"
	"testing"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	serverapi "huatuo-bamai/apis/v1/server"
	profilinghandler "huatuo-bamai/cmd/huatuo-apiserver/handlers/profiling"
	"huatuo-bamai/internal/job"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/observation"
	tracingdomain "huatuo-bamai/pkg/tracing"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

func TestGetReadiness(t *testing.T) {
	response, err := (&APIHandler{}).GetReadiness(
		t.Context(),
		serverapi.GetReadinessRequestObject{},
	)
	if err != nil {
		t.Fatalf("GetReadiness() error = %v", err)
	}
	if _, ok := response.(serverapi.GetReadiness204Response); !ok {
		t.Fatalf("GetReadiness() response = %T, want GetReadiness204Response", response)
	}
}

func TestCommonJobMapsFailureReasonWithoutAddingJobState(t *testing.T) {
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	jobEntity := &job.Job{
		ID:        "job-1",
		Hostname:  "node-1",
		Duration:  time.Minute,
		Scope:     observation.ScopeHost,
		Status:    job.StatusFailed,
		CreatedAt: base,
		UpdatedAt: base.Add(time.Second),
		EndedAt:   base.Add(time.Minute),
		Failure: &job.TerminalFailure{
			Reason:  job.FailureReasonOperationLost,
			Message: "Node no longer has the Operation",
		},
	}

	got := commonJob(jobEntity)
	if got.Status != serverapi.JobStatusFailed || got.Failure == nil {
		t.Fatalf("commonJob() = %+v", got)
	}
	if got.Failure.Code != apiv1.ErrorCode(job.FailureReasonOperationLost) {
		t.Fatalf("failure code = %q", got.Failure.Code)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(jobEntity.EndedAt) {
		t.Fatalf("ended_at = %v", got.EndedAt)
	}
}

func TestTracingJobUsesIndependentDomainState(t *testing.T) {
	jobEntity := &job.Job{
		ID:       "job-1",
		Kind:     job.KindTracing,
		Hostname: "node-1",
		Duration: time.Minute,
		Scope:    observation.ScopeHost,
		Status:   job.StatusOutcomeUnknown,
		Spec: job.Spec{Tracing: &tracingdomain.Spec{
			Type: tracingdomain.TypeNetworkingDrop,
		}},
	}
	got, err := tracingJob(jobEntity)
	if err != nil {
		t.Fatalf("tracingJob() error = %v", err)
	}
	if got.Status != serverapi.JobStatusOutcomeUnknown || got.Failure != nil {
		t.Fatalf("tracingJob() = %+v", got)
	}
}

func TestServerAPIErrorMapsStableResultErrors(t *testing.T) {
	tests := []struct {
		err      error
		wantCode apiv1.ErrorCode
	}{
		{err: profilinghandler.ErrResultNotReady, wantCode: serverapi.ErrorCodeResultNotReady},
		{err: profilinghandler.ErrResultNotFound, wantCode: serverapi.ErrorCodeResultNotFound},
		{err: profilinghandler.ErrResultUnavailable, wantCode: serverapi.ErrorCodeResultUnavailable},
		{err: errRawProfileResponseTooLarge, wantCode: serverapi.ErrorCodeResultTooLarge},
		{err: job.ErrQuotaExceeded, wantCode: serverapi.ErrorCodeQuotaExceeded},
		{err: job.ErrNotFound, wantCode: serverapi.ErrorCodeJobNotFound},
	}
	for _, tt := range tests {
		mapped := serverAPIError(tt.err)
		var apiErr *response.APIError
		if !errors.As(mapped, &apiErr) || apiErr.Code != tt.wantCode {
			t.Fatalf("serverAPIError(%v) = %v, want code %q", tt.err, mapped, tt.wantCode)
		}
	}
}

func TestRawProfilesRejectsOversizedEncodedPage(t *testing.T) {
	_, err := rawProfiles([]*profilinghandler.RawProfile{{
		Profile: &profilev1.Profile{StringTable: []string{"profile payload"}},
	}}, 1)
	if !errors.Is(err, errRawProfileResponseTooLarge) {
		t.Fatalf("rawProfiles() error = %v, want ErrResponseTooLarge", err)
	}
}
