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
	"os"
	"path/filepath"
	"testing"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/nodeagent/operation"
	nodeprofiling "huatuo-bamai/internal/nodeagent/profiling"
	nodetracing "huatuo-bamai/internal/nodeagent/tracing"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/internal/toolstream"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"
)

type testResultPublisher struct{}

func (testResultPublisher) Publish(context.Context, string) error {
	return nil
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

func newTestNodeServices(
	t *testing.T,
	manager *operation.Manager,
) (*nodeprofiling.Service, *nodetracing.Service) {
	t.Helper()
	directory := t.TempDir()
	profilerPath := filepath.Join(directory, "profiler")
	if err := os.WriteFile(profilerPath, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() profiler error = %v", err)
	}
	if err := os.Chmod(profilerPath, 0o700); err != nil {
		t.Fatalf("Chmod() profiler error = %v", err)
	}
	socketPath := filepath.Join(directory, "toolstream.sock")
	stream, err := toolstream.NewServer(socketPath)
	if err != nil {
		t.Fatalf("toolstream.NewServer() error = %v", err)
	}
	profilingService, err := nodeprofiling.NewService(manager, &nodeprofiling.Config{
		ProfilerPath:            profilerPath,
		ToolstreamSocketPath:    socketPath,
		NodeAPIAddress:          "http://127.0.0.1",
		AggregationInterval:     time.Second,
		MaxConcurrentProcesses:  1,
		CommandOutputLimitBytes: 1024,
		ToolstreamServer:        stream,
		ResultPublisher:         testResultPublisher{},
	})
	if err != nil {
		t.Fatalf("profiling.NewService() error = %v", err)
	}
	tracingService, err := nodetracing.NewService(manager)
	if err != nil {
		t.Fatalf("tracing.NewService() error = %v", err)
	}
	return profilingService, tracingService
}

func TestNewNodeAPIHandlerRequiresDependencies(t *testing.T) {
	manager := newTestOperationManager(t)
	profilingService, tracingService := newTestNodeServices(t, manager)
	tests := []struct {
		name      string
		manager   *operation.Manager
		profiling *nodeprofiling.Service
		tracing   *nodetracing.Service
	}{
		{name: "missing operation manager", profiling: profilingService, tracing: tracingService},
		{name: "missing profiling service", manager: manager, tracing: tracingService},
		{name: "missing tracing service", manager: manager, profiling: profilingService},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewNodeAPIHandler(tt.manager, tt.profiling, tt.tracing); err == nil {
				t.Fatal("NewNodeAPIHandler() error = nil")
			}
		})
	}
}

func TestStartOperationReturnsAcceptedOnlyForNewOperation(t *testing.T) {
	manager := newTestOperationManager(t)
	profilingService, tracingService := newTestNodeServices(t, manager)
	handler, err := NewNodeAPIHandler(
		manager,
		profilingService,
		tracingService,
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
	spec := nodeapi.ProfilingOperationSpec{
		Type: nodeapi.ProfilingTypeCPU, Language: nodeapi.ProfilingLanguageGo,
		Mode: nodeapi.ProfilingModeOnCPU,
	}
	if err := body.Spec.FromProfilingOperationSpec(spec); err != nil {
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
	profilingRequest, err := profilingOperationRequest(body, spec)
	if err != nil {
		t.Fatalf("profilingOperationRequest() error = %v", err)
	}
	if profilingRequest.Duration != time.Minute ||
		profilingRequest.Scope != observation.ScopeHost ||
		profilingRequest.Spec.Type != profilingdomain.TypeCPU {
		t.Fatalf("profilingOperationRequest() = %+v", profilingRequest)
	}

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
	manager := newTestOperationManager(t)
	profilingService, tracingService := newTestNodeServices(t, manager)
	handler, err := NewNodeAPIHandler(
		manager,
		profilingService,
		tracingService,
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
