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
	"huatuo-bamai/internal/auth"
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

func (s *stubProfilingOperations) Get(string) (*operation.Operation, error) {
	return s.operation, s.err
}

func (s *stubProfilingOperations) Stop(string) (*operation.Operation, bool, error) {
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

func (s *stubTracingOperations) Get(string) (*operation.Operation, error) {
	return s.operation, s.err
}

func (s *stubTracingOperations) Stop(string) (*operation.Operation, bool, error) {
	return s.operation, s.created, s.err
}

func nodeRequestContext(t *testing.T) context.Context {
	t.Helper()
	return auth.WithPrincipal(t.Context(), auth.Principal{ID: nodePrincipalID})
}

func TestStartProfilingReturnsAcceptedOnlyForNewOperation(t *testing.T) {
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
	handler, err := NewNodeAPIHandler(profiling, &stubTracingOperations{})
	if err != nil {
		t.Fatalf("NewNodeAPIHandler() error = %v", err)
	}
	body := &nodeapi.StartProfilingJSONRequestBody{
		RequestID:       "job-1",
		DurationSeconds: 60,
		Scope:           apiv1.ObservationScopeHost,
		Type:            nodeapi.ProfilingTypeCPU,
		Language:        nodeapi.ProfilingLanguageGo,
		Mode:            nodeapi.ProfilingModeOnCPU,
	}

	got, err := handler.StartProfiling(
		nodeRequestContext(t),
		nodeapi.StartProfilingRequestObject{Body: body},
	)
	if err != nil {
		t.Fatalf("StartProfiling() error = %v", err)
	}
	if _, ok := got.(nodeapi.StartProfiling202JSONResponse); !ok {
		t.Fatalf("StartProfiling() response type = %T, want HTTP 202", got)
	}
	if profiling.startRequest == nil || profiling.startRequest.Duration != time.Minute ||
		profiling.startRequest.Scope != observation.ScopeHost {
		t.Fatalf("Start() request = %+v", profiling.startRequest)
	}

	profiling.created = false
	got, err = handler.StartProfiling(
		nodeRequestContext(t),
		nodeapi.StartProfilingRequestObject{Body: body},
	)
	if err != nil {
		t.Fatalf("idempotent StartProfiling() error = %v", err)
	}
	if _, ok := got.(nodeapi.StartProfiling200JSONResponse); !ok {
		t.Fatalf("idempotent StartProfiling() response type = %T, want HTTP 200", got)
	}
}

func TestNodeAPIRequiresServicePrincipal(t *testing.T) {
	handler, err := NewNodeAPIHandler(
		&stubProfilingOperations{},
		&stubTracingOperations{},
	)
	if err != nil {
		t.Fatalf("NewNodeAPIHandler() error = %v", err)
	}
	_, err = handler.GetProfiling(
		t.Context(),
		nodeapi.GetProfilingRequestObject{RequestID: "job-1"},
	)
	var apiErr *response.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != apiv1.ErrorCodeUnauthenticated {
		t.Fatalf("GetProfiling() error = %v", err)
	}
}

func TestStartTracingReportsNotImplemented(t *testing.T) {
	handler, err := NewNodeAPIHandler(
		&stubProfilingOperations{},
		&stubTracingOperations{err: nodetracing.ErrNotImplemented},
	)
	if err != nil {
		t.Fatalf("NewNodeAPIHandler() error = %v", err)
	}
	_, err = handler.StartTracing(nodeRequestContext(t), nodeapi.StartTracingRequestObject{
		Body: &nodeapi.StartTracingJSONRequestBody{
			RequestID:       "job-1",
			DurationSeconds: 60,
			Scope:           apiv1.ObservationScopeHost,
			Type:            nodeapi.TracingTypeNetworkingDrop,
		},
	})
	var apiErr *response.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != nodeapi.ErrorCodeServiceNotImplemented {
		t.Fatalf("StartTracing() error = %v", err)
	}
}

func TestOperationResponseMapsFailureReason(t *testing.T) {
	payload, err := operationResponse(&operation.Operation{
		RequestID: "job-1",
		Kind:      operation.KindProfiling,
		Status:    operation.StatusFailed,
		Failure: &operation.TerminalFailure{
			Reason:  operation.FailureReasonFinalizationTimeout,
			Message: "result stream timed out",
		},
		CreatedAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("operationResponse() error = %v", err)
	}
	if payload.Data.Status != nodeapi.OperationStatusFailed || payload.Data.Failure == nil ||
		payload.Data.Failure.Code != nodeapi.ErrorCodeFinalizationTimeout {
		t.Fatalf("operationResponse() = %+v", payload.Data)
	}
}

func TestSecondsDurationRejectsOverflow(t *testing.T) {
	if _, err := secondsDuration(0); err == nil {
		t.Fatal("secondsDuration(0) error = nil")
	}
	if _, err := secondsDuration(int64(^uint64(0) >> 1)); err == nil {
		t.Fatal("secondsDuration(MaxInt64) error = nil")
	}
}
