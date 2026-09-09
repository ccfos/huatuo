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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/nodeagent/operation"
	nodeprofiling "huatuo-bamai/internal/nodeagent/profiling"
	nodetracing "huatuo-bamai/internal/nodeagent/tracing"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"
	tracingdomain "huatuo-bamai/pkg/tracing"
)

// NodeAPIHandler implements the generated Node Agent Strict Server.
type NodeAPIHandler struct {
	operationManager *operation.Manager
	profiling        *nodeprofiling.Service
	tracing          *nodetracing.Service
	containerByID    func(string) (*pod.Container, error)
	openAPI          nodeapi.GetOpenAPI200JSONResponse
}

// StartOperation starts or resolves an idempotent Node Operation.
func (h *NodeAPIHandler) StartOperation(
	ctx context.Context,
	request nodeapi.StartOperationRequestObject,
) (nodeapi.StartOperationResponseObject, error) {
	kind := request.Body.Kind
	if kind != nodeapi.OperationKindProfiling && kind != nodeapi.OperationKindTracing {
		return nil, nodeAPIError(fmt.Errorf("unsupported operation kind %q", kind))
	}
	duration, err := secondsDuration(request.Body.DurationSeconds)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("%w: %w", operation.ErrInvalidRequest, err))
	}

	var (
		snapshot *operation.Operation
		created  bool
	)
	switch kind {
	case nodeapi.OperationKindProfiling:
		snapshot, created, err = h.startProfilingOperation(ctx, request.Body, duration)
	case nodeapi.OperationKindTracing:
		snapshot, created, err = h.startTracingOperation(ctx, request.Body, duration)
	}
	if err != nil {
		return nil, nodeAPIError(err)
	}

	payload := operationResponse(snapshot)
	if created {
		return nodeapi.StartOperation202JSONResponse(payload), nil
	}
	return nodeapi.StartOperation200JSONResponse(payload), nil
}

func (h *NodeAPIHandler) startProfilingOperation(
	ctx context.Context,
	body *nodeapi.StartOperationJSONRequestBody,
	duration time.Duration,
) (*operation.Operation, bool, error) {
	spec, err := body.Spec.AsProfilingOperationSpec()
	if err != nil {
		return nil, false, fmt.Errorf("decode profiling operation spec: %w", err)
	}
	request := nodeprofiling.StartRequest{
		RequestID:   body.RequestID,
		Duration:    duration,
		Scope:       observation.Scope(body.Scope),
		ContainerID: optionalString(body.ContainerID),
		Spec: profilingdomain.Spec{
			Type:            profilingdomain.Type(spec.Type),
			Language:        profilingdomain.Language(spec.Language),
			Mode:            profilingdomain.Mode(spec.Mode),
			BinaryMatchPath: optionalString(spec.BinaryMatchPath),
		},
	}
	snapshot, created, err := h.profiling.Start(ctx, &request)
	if err != nil {
		return nil, false, fmt.Errorf("start profiling operation: %w", err)
	}
	return snapshot, created, nil
}

func (h *NodeAPIHandler) startTracingOperation(
	ctx context.Context,
	body *nodeapi.StartOperationJSONRequestBody,
	duration time.Duration,
) (*operation.Operation, bool, error) {
	spec, err := body.Spec.AsTracingOperationSpec()
	if err != nil {
		return nil, false, fmt.Errorf("decode tracing operation spec: %w", err)
	}
	request := nodetracing.StartRequest{
		RequestID:   body.RequestID,
		Duration:    duration,
		Scope:       observation.Scope(body.Scope),
		ContainerID: optionalString(body.ContainerID),
		Spec: tracingdomain.Spec{
			Type: tracingdomain.Type(spec.Type),
		},
	}
	snapshot, created, err := h.tracing.Start(ctx, &request)
	if err != nil {
		return nil, false, fmt.Errorf("start tracing operation: %w", err)
	}
	return snapshot, created, nil
}

// GetOperation returns a retained Node Operation of any supported kind.
func (h *NodeAPIHandler) GetOperation(
	ctx context.Context,
	request nodeapi.GetOperationRequestObject,
) (nodeapi.GetOperationResponseObject, error) {
	snapshot, err := h.operationManager.GetByID(request.RequestID)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("get operation: %w", err))
	}
	payload := operationResponse(snapshot)
	return nodeapi.GetOperation200JSONResponse(payload), nil
}

// StopOperation records an asynchronous stop intent for any supported kind.
func (h *NodeAPIHandler) StopOperation(
	ctx context.Context,
	request nodeapi.StopOperationRequestObject,
) (nodeapi.StopOperationResponseObject, error) {
	snapshot, initiated, err := h.operationManager.StopByID(request.RequestID)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("stop operation: %w", err))
	}
	payload := operationResponse(snapshot)
	if initiated {
		return nodeapi.StopOperation202JSONResponse(payload), nil
	}
	return nodeapi.StopOperation200JSONResponse(payload), nil
}

// NewNodeAPIHandler constructs a generated-protocol adapter.
func NewNodeAPIHandler(
	operationManager *operation.Manager,
	profilingService *nodeprofiling.Service,
	tracingService *nodetracing.Service,
) (*NodeAPIHandler, error) {
	if err := validateNewNodeAPIHandlerArgs(
		operationManager,
		profilingService,
		tracingService,
	); err != nil {
		return nil, err
	}
	var specification map[string]any
	if err := json.Unmarshal(nodeapi.OpenAPIJSON(), &specification); err != nil {
		return nil, fmt.Errorf("create Node API handler: decode bundled OpenAPI: %w", err)
	}
	return &NodeAPIHandler{
		operationManager: operationManager,
		profiling:        profilingService,
		tracing:          tracingService,
		containerByID:    pod.ContainerByID,
		openAPI:          specification,
	}, nil
}

func validateNewNodeAPIHandlerArgs(
	operationManager *operation.Manager,
	profilingService *nodeprofiling.Service,
	tracingService *nodetracing.Service,
) error {
	if operationManager == nil {
		return errors.New("create Node API handler: operation manager is required")
	}
	if profilingService == nil {
		return errors.New("create Node API handler: profiling service is required")
	}
	if tracingService == nil {
		return errors.New("create Node API handler: tracing service is required")
	}
	return nil
}

// GetReadiness reports whether the HTTP server is ready to accept requests.
func (*NodeAPIHandler) GetReadiness(
	context.Context,
	nodeapi.GetReadinessRequestObject,
) (nodeapi.GetReadinessResponseObject, error) {
	return nodeapi.GetReadiness204Response{}, nil
}

// GetOpenAPI returns the bundled protocol document.
func (h *NodeAPIHandler) GetOpenAPI(
	context.Context,
	nodeapi.GetOpenAPIRequestObject,
) (nodeapi.GetOpenAPIResponseObject, error) {
	return h.openAPI, nil
}

func secondsDuration(seconds int64) (time.Duration, error) {
	if seconds > math.MaxInt64/int64(time.Second) {
		return 0, errors.New("duration seconds exceed the supported range")
	}
	return time.Duration(seconds) * time.Second, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func operationResponse(snapshot *operation.Operation) nodeapi.OperationResponse {
	payload := nodeapi.Operation{
		RequestID:  snapshot.RequestID,
		Kind:       nodeapi.OperationKind(snapshot.Kind),
		Status:     nodeapi.OperationStatus(snapshot.Status),
		CreatedAt:  snapshot.CreatedAt,
		StartedAt:  snapshot.StartedAt,
		FinishedAt: snapshot.FinishedAt,
	}
	payload.Terminal = operationTerminal(snapshot)
	return nodeapi.OperationResponse{Data: payload}
}

func operationTerminal(snapshot *operation.Operation) *nodeapi.OperationTerminal {
	terminalResult := snapshot.Terminal
	if snapshot.Status != operation.StatusTerminal {
		return nil
	}
	terminal := &nodeapi.OperationTerminal{
		Outcome: nodeapi.OperationOutcome(terminalResult.Outcome),
	}
	if terminalResult.Reason != "" {
		reason := string(terminalResult.Reason)
		message := terminalResult.Message
		terminal.Reason = &reason
		terminal.Message = &message
	}
	return terminal
}

func nodeAPIError(err error) error {
	switch {
	case errors.Is(err, nodeprofiling.ErrInvalidRequest),
		errors.Is(err, nodetracing.ErrInvalidRequest),
		errors.Is(err, operation.ErrInvalidRequest):
		return response.NewAPIError(apiv1.ErrorCodeInvalidRequest, err.Error())
	case errors.Is(err, nodeprofiling.ErrEnvironmentUnsupported):
		return response.NewAPIError(nodeapi.ErrorCodeExecutionEnvironmentUnsupported, err.Error())
	case errors.Is(err, nodetracing.ErrNotImplemented):
		return response.NewAPIError(nodeapi.ErrorCodeServiceNotImplemented, "tracing service is not implemented")
	case errors.Is(err, operation.ErrNotFound):
		return response.NewAPIError(nodeapi.ErrorCodeOperationNotFound, "operation was not found")
	case errors.Is(err, operation.ErrRequestIDConflict):
		return response.NewAPIError(nodeapi.ErrorCodeRequestIDConflict, "request ID belongs to another operation kind")
	case errors.Is(err, operation.ErrLimitExceeded):
		return response.NewAPIError(nodeapi.ErrorCodeOperationLimitExceeded, "operation capacity is exhausted")
	case errors.Is(err, operation.ErrShuttingDown):
		return response.NewAPIError(apiv1.ErrorCodeServiceUnavailable, "node agent is shutting down")
	default:
		return err
	}
}
