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
	"testing"
	"time"

	apiv1 "github.com/ccfos/huatuo/apis/v1"
	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	"github.com/ccfos/huatuo/client"
)

func TestRuntimeDistinguishesUnknownAndLostOperations(t *testing.T) {
	tests := []struct {
		name       string
		startedAt  time.Time
		wantStatus Status
		wantReason FailureReason
	}{
		{
			name:       "operation was never observed running",
			wantStatus: StatusTerminal,
		},
		{
			name:       "previously running operation disappeared",
			startedAt:  time.Date(2026, 8, 24, 11, 59, 0, 0, time.UTC),
			wantStatus: StatusTerminal,
			wantReason: FailureReasonOperationLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
			pendingJob := testJob("job-1", StatusPending, now)
			pendingJob.StartedAt = tt.startedAt
			if !tt.startedAt.IsZero() {
				pendingJob.Status = StatusRunning
				pendingJob.ExecutionDeadline = now.Add(time.Minute)
			}
			store := newMemoryStore(pendingJob)
			manager := testManager(store, &stubNodeClient{})
			setManagerNow(manager, func() time.Time { return now.Add(time.Second) })
			runtime := testRuntime(manager, pendingJob)
			nodeErr := &client.NodeError{
				StatusCode: 404,
				Code:       nodeapi.ErrorCodeOperationNotFound,
				Message:    "operation not found",
			}

			terminal, err := runtime.reconcileJobWithError(t.Context(), nodeErr)
			if err != nil || !terminal {
				t.Fatalf("reconcileJobWithError() = (%t, %v)", terminal, err)
			}
			got := runtime.snapshot()
			if got.Status != StatusTerminal {
				t.Fatalf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if tt.startedAt.IsZero() && got.Terminal.Outcome != OutcomeUnknown {
				t.Fatalf("outcome = %q, want unknown", got.Terminal.Outcome)
			}
			if !tt.startedAt.IsZero() && got.Terminal.Outcome != OutcomeFailed {
				t.Fatalf("outcome = %q, want failed", got.Terminal.Outcome)
			}
			if tt.wantReason == "" {
			} else if got.Terminal == nil || got.Terminal.Reason != tt.wantReason {
				t.Fatalf("terminal = %+v, want reason %q", got.Terminal, tt.wantReason)
			}
		})
	}
}

func TestRuntimeReconcileJobWithErrorClassifiesErrors(t *testing.T) {
	tests := []struct {
		name                    string
		err                     error
		wantTerminal            bool
		wantStatus              Status
		wantReason              FailureReason
		wantUnavailableDeadline bool
	}{
		{
			name: "start capacity failure",
			err: &client.NodeError{
				StatusCode: 429,
				Code:       nodeapi.ErrorCodeOperationLimitExceeded,
				Message:    "profiling capacity exhausted",
			},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonExecutionCapacityExceeded,
		},
		{
			name: "execution start failure",
			err: &client.NodeError{
				StatusCode: 500,
				Code:       nodeapi.ErrorCodeExecutionStartFailed,
				Message:    "invalid operation state",
			},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonExecutionStartFailed,
		},
		{
			name:         "client protocol error",
			err:          &client.NodeError{Code: client.NodeErrorCodeProtocol},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonProtocolError,
		},
		{
			name: "client invalid argument",
			err: &client.NodeError{
				Code:    client.NodeErrorCodeInvalidArgument,
				Message: "invalid Node host",
			},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonInvalidNodeRequest,
		},
		{
			name:                    "client transport error",
			err:                     &client.NodeError{Code: client.NodeErrorCodeTransport},
			wantStatus:              StatusPending,
			wantUnavailableDeadline: true,
		},
		{
			name: "node unavailable",
			err: &client.NodeError{
				StatusCode: 503,
				Code:       apiv1.ErrorCodeServiceUnavailable,
				Message:    "service unavailable",
			},
			wantStatus:              StatusPending,
			wantUnavailableDeadline: true,
		},
		{
			name: "execution failure",
			err: &client.NodeError{
				StatusCode: 500,
				Code:       nodeapi.ErrorCodeExecutionFailed,
				Message:    "executor failed",
			},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonExecutionFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
			pendingJob := testJob("job-1", StatusPending, now)
			manager := testManager(newMemoryStore(pendingJob), &stubNodeClient{})
			setManagerNow(manager, func() time.Time { return now })
			runtime := testRuntime(manager, pendingJob)

			terminal, err := runtime.reconcileJobWithError(t.Context(), tt.err)
			if err != nil || terminal != tt.wantTerminal {
				t.Fatalf(
					"reconcileJobWithError() = (%t, %v), want (%t, nil)",
					terminal,
					err,
					tt.wantTerminal,
				)
			}
			got := runtime.snapshot()
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if tt.wantReason != "" && (got.Terminal == nil || got.Terminal.Reason != tt.wantReason) {
				t.Fatalf("terminal = %+v, want reason %q", got.Terminal, tt.wantReason)
			}
			wantDeadline := time.Time{}
			if tt.wantUnavailableDeadline {
				wantDeadline = now.Add(time.Minute)
			}
			if !got.NodeUnavailableDeadline.Equal(wantDeadline) {
				t.Fatalf(
					"NodeUnavailableDeadline = %s, want %s",
					got.NodeUnavailableDeadline,
					wantDeadline,
				)
			}
		})
	}
}

func TestRuntimeReconcileJobWithErrorRejectsUnstructuredError(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	runtime := testRuntime(
		testManager(newMemoryStore(pendingJob), &stubNodeClient{}),
		pendingJob,
	)

	terminal, err := runtime.reconcileJobWithError(
		t.Context(),
		errors.New("Node unavailable"),
	)
	if err == nil || terminal {
		t.Fatalf(
			"reconcileJobWithError() = (%t, %v), want (false, error)",
			terminal,
			err,
		)
	}
	if got := runtime.snapshot(); got.Status != StatusPending ||
		!got.NodeUnavailableDeadline.IsZero() {
		t.Fatalf("Job = (%q, %s), want unchanged pending Job", got.Status, got.NodeUnavailableDeadline)
	}
}
