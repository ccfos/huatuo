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
	"context"
	"errors"
	"testing"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/nodeagent/operation"
	nodeprofiling "huatuo-bamai/internal/nodeagent/profiling"
	nodetracing "huatuo-bamai/internal/nodeagent/tracing"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/observation"
)

type stubProfilingOperations struct {
	startRequest *nodeprofiling.StartRequest
	operation    *operation.Operation
	created      bool
	err          error
}

func (s *stubProfilingOperations) Start(
	_ context.Context,
	request *nodeprofiling.StartRequest,
) (*operation.Operation, bool, error) {
	s.startRequest = request
	return s.operation, s.created, s.err
}

type stubTracingOperations struct {
	operation *operation.Operation
	created   bool
	err       error
}

func (s *stubTracingOperations) Start(
	context.Context,
	nodetracing.StartRequest,
) (*operation.Operation, bool, error) {
	return s.operation, s.created, s.err
}

func newTestOperationManager(t *testing.T) *operation.Manager {
	t.Helper()
	manager, err := operation.NewManager(operation.Config{
		MaxConcurrent: 1,
		Lifecycle: operation.LifecyclePolicy{
			LaunchTimeout:           time.Second,
			StopGracePeriod:         time.Second,
			FinalizationTimeout:     time.Second,
			TerminalRetentionPeriod: time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("operation.NewManager() error = %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Shutdown(context.Background()); err != nil {
			t.Errorf("operation.Manager.Shutdown() error = %v", err)
		}
	})
	return manager
}

func TestStartOperationReturnsAcceptedOnlyForNewOperation(t *testing.T) {
	base := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	profiling := &stubProfilingOperations{
		operation: &operation.Operation{
			RequestID: "job-1",
			Kind:      operation.KindProfiling,
			Status:    operation.StatusPending,
			CreatedAt: base,
		},
		created: true,
	}
	handler, err := NewNodeAPIHandler(
		newTestOperationManager(t), profiling, &stubTracingOperations{},
	)
	if err != nil {
		t.Fatalf("NewNodeAPIHandler() error = %v", err)
	}
	body := &nodeapi.StartOperationJSONRequestBody{
		RequestID:       "job-1",
		DurationSeconds: 60,
		Scope:           apiv1.ObservationScopeHost,
		Kind:            nodeapi.OperationKindProfiling,
	}
	if err := body.Spec.FromProfilingOperationSpec(nodeapi.ProfilingOperationSpec{
		Type: nodeapi.ProfilingTypeCPU, Language: nodeapi.ProfilingLanguageGo,
		Mode: nodeapi.ProfilingModeOnCPU,
	}); err != nil {
		t.Fatalf("set profiling spec: %v", err)
	}

	got, err := handler.StartOperation(
		t.Context(),
		nodeapi.StartOperationRequestObject{Body: body},
	)
	if err != nil {
		t.Fatalf("StartOperation() error = %v", err)
	}
	if _, ok := got.(nodeapi.StartOperation202JSONResponse); !ok {
		t.Fatalf("StartOperation() response type = %T, want HTTP 202", got)
	}
	if profiling.startRequest == nil || profiling.startRequest.Duration != time.Minute ||
		profiling.startRequest.Scope != observation.ScopeHost {
		t.Fatalf("StartOperation() request = %+v", profiling.startRequest)
	}

	profiling.created = false
	got, err = handler.StartOperation(
		t.Context(),
		nodeapi.StartOperationRequestObject{Body: body},
	)
	if err != nil {
		t.Fatalf("idempotent StartOperation() error = %v", err)
	}
	if _, ok := got.(nodeapi.StartOperation200JSONResponse); !ok {
		t.Fatalf("idempotent StartOperation() response type = %T, want HTTP 200", got)
	}
}

func TestStartOperationTracingReportsNotImplemented(t *testing.T) {
	handler, err := NewNodeAPIHandler(
		newTestOperationManager(t),
		&stubProfilingOperations{},
		&stubTracingOperations{err: nodetracing.ErrNotImplemented},
	)
	if err != nil {
		t.Fatalf("NewNodeAPIHandler() error = %v", err)
	}
	body := &nodeapi.StartOperationJSONRequestBody{
		RequestID:       "job-1",
		DurationSeconds: 60,
		Scope:           apiv1.ObservationScopeHost,
		Kind:            nodeapi.OperationKindTracing,
	}
	if err := body.Spec.FromTracingOperationSpec(nodeapi.TracingOperationSpec{
		Type: nodeapi.TracingTypeNetworkingDrop,
	}); err != nil {
		t.Fatalf("set tracing spec: %v", err)
	}
	_, err = handler.StartOperation(t.Context(), nodeapi.StartOperationRequestObject{Body: body})
	var apiErr *response.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != nodeapi.ErrorCodeServiceNotImplemented {
		t.Fatalf("StartOperation() error = %v", err)
	}
}

func TestOperationResponseMapsFailureReason(t *testing.T) {
	payload := operationResponse(&operation.Operation{
		RequestID: "job-1",
		Kind:      operation.KindProfiling,
		Status:    operation.StatusTerminal,
		Terminal: &operation.TerminalResult{
			Outcome: operation.OutcomeFailed,
			Reason:  operation.FailureReasonFinalizationTimeout,
			Message: "result stream timed out",
		},
		CreatedAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	})
	if payload.Data.Status != nodeapi.OperationStatusTerminal || payload.Data.Terminal == nil ||
		payload.Data.Terminal.Outcome != nodeapi.OperationOutcomeFailed ||
		payload.Data.Terminal.Reason == nil ||
		*payload.Data.Terminal.Reason != string(operation.FailureReasonFinalizationTimeout) {
		t.Fatalf("operationResponse() = %+v", payload.Data)
	}
}

func TestSecondsDurationRejectsOverflow(t *testing.T) {
	if _, err := secondsDuration(int64(^uint64(0) >> 1)); err == nil {
		t.Fatal("secondsDuration(MaxInt64) error = nil")
	}
}
