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
	"strings"
	"testing"
	"time"

	apiv1 "github.com/ccfos/huatuo/apis/v1"
	serverapi "github.com/ccfos/huatuo/apis/v1/server"
	profilinghandler "github.com/ccfos/huatuo/cmd/huatuo-apiserver/handlers/profiling"
	"github.com/ccfos/huatuo/internal/job"
	"github.com/ccfos/huatuo/internal/server/response"
	"github.com/ccfos/huatuo/pkg/observation"
	tracingdomain "github.com/ccfos/huatuo/pkg/tracing"

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

func TestStartRejectsMissingAuthUsers(t *testing.T) {
	_, err := Start(&ServerOptions{JobManager: &job.Manager{}})
	if err == nil || !strings.Contains(err.Error(), "at least one auth user is required") {
		t.Fatalf("Start() error = %v, want missing auth user error", err)
	}
}

func TestMapCommonJobMapsTerminalFailure(t *testing.T) {
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	input := &job.Job{
		ID:        "job-1",
		Hostname:  "node-1",
		Duration:  time.Minute,
		Scope:     observation.ScopeHost,
		Status:    job.StatusTerminal,
		CreatedAt: base,
		UpdatedAt: base.Add(time.Second),
		EndedAt:   base.Add(time.Minute),
		Terminal: &job.TerminalResult{
			Outcome: job.OutcomeFailed,
			Reason:  job.FailureReasonOperationLost,
			Message: "Node no longer has the Operation",
		},
	}

	got := mapCommonJob(input)
	if got.Status != serverapi.JobStatusTerminal || got.Terminal == nil {
		t.Fatalf("mapCommonJob() = %+v", got)
	}
	if got.Terminal.Outcome != serverapi.JobOutcomeFailed || got.Terminal.Reason == nil ||
		*got.Terminal.Reason != string(job.FailureReasonOperationLost) {
		t.Fatalf("terminal = %+v", got.Terminal)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(input.EndedAt) {
		t.Fatalf("ended_at = %v", got.EndedAt)
	}
}

func TestMapTracingJobUsesIndependentDomainState(t *testing.T) {
	input := &job.Job{
		ID:       "job-1",
		Kind:     job.KindTracing,
		Hostname: "node-1",
		Duration: time.Minute,
		Scope:    observation.ScopeHost,
		Status:   job.StatusTerminal,
		Terminal: &job.TerminalResult{Outcome: job.OutcomeUnknown},
		Spec: job.Spec{Tracing: &tracingdomain.Spec{
			Type: tracingdomain.TypeNetworkingDrop,
		}},
	}
	got := mapTracingJob(input)
	if got.Status != serverapi.JobStatusTerminal || got.Terminal == nil ||
		got.Terminal.Outcome != serverapi.JobOutcomeUnknown {
		t.Fatalf("mapTracingJob() = %+v", got)
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
		{err: job.ErrJobNotSupervised, wantCode: apiv1.ErrorCodeServiceUnavailable},
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
