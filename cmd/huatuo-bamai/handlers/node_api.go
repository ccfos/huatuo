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
	"huatuo-bamai/internal/auth"
	"huatuo-bamai/internal/nodeagent/operation"
	nodeprofiling "huatuo-bamai/internal/nodeagent/profiling"
	nodetracing "huatuo-bamai/internal/nodeagent/tracing"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"
	tracingdomain "huatuo-bamai/pkg/tracing"
)

const nodePrincipalID = "apiserver"

type profilingOperationService interface {
	Start(
		ctx context.Context,
		request *nodeprofiling.StartRequest,
	) (operationSnapshot *operation.Operation, created bool, err error)
}

type tracingOperationService interface {
	Start(
		ctx context.Context,
		request nodetracing.StartRequest,
	) (operationSnapshot *operation.Operation, created bool, err error)
}

// NodeAPIHandler implements the generated Node Agent Strict Server.
type NodeAPIHandler struct {
	operationManager *operation.Manager
	profiling        profilingOperationService
	tracing          tracingOperationService
	openAPI          nodeapi.GetOpenAPI200JSONResponse
}

// StartOperation starts or resolves an idempotent Node Operation.
func (h *NodeAPIHandler) StartOperation(
	ctx context.Context,
	request nodeapi.StartOperationRequestObject,
) (nodeapi.StartOperationResponseObject, error) {
	if err := requireNodePrincipal(ctx); err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, nodeAPIError(errors.New("operation request body is required"))
	}
	switch request.Body.Kind {
	case nodeapi.OperationKindProfiling:
		spec, err := request.Body.Spec.AsProfilingOperationSpec()
		if err != nil {
			return nil, nodeAPIError(fmt.Errorf("decode profiling operation spec: %w", err))
		}
		profilingRequest, err := profilingOperationRequest(request.Body, spec)
		if err != nil {
			return nil, nodeAPIError(err)
		}
		snapshot, created, err := h.profiling.Start(ctx, &profilingRequest)
		if err != nil {
			return nil, nodeAPIError(fmt.Errorf("start profiling operation: %w", err))
		}
		payload, err := operationResponse(snapshot)
		if err != nil {
			return nil, fmt.Errorf("start profiling operation: %w", err)
		}
		if created {
			return nodeapi.StartOperation202JSONResponse(payload), nil
		}
		return nodeapi.StartOperation200JSONResponse(payload), nil
	case nodeapi.OperationKindTracing:
		spec, err := request.Body.Spec.AsTracingOperationSpec()
		if err != nil {
			return nil, nodeAPIError(fmt.Errorf("decode tracing operation spec: %w", err))
		}
		tracingRequest, err := tracingOperationRequest(request.Body, spec)
		if err != nil {
			return nil, nodeAPIError(err)
		}
		snapshot, created, err := h.tracing.Start(ctx, tracingRequest)
		if err != nil {
			return nil, nodeAPIError(fmt.Errorf("start tracing operation: %w", err))
		}
		payload, err := operationResponse(snapshot)
		if err != nil {
			return nil, fmt.Errorf("start tracing operation: %w", err)
		}
		if created {
			return nodeapi.StartOperation202JSONResponse(payload), nil
		}
		return nodeapi.StartOperation200JSONResponse(payload), nil
	default:
		return nil, nodeAPIError(fmt.Errorf("unsupported operation kind %q", request.Body.Kind))
	}
}

// GetOperation returns a retained Node Operation of any supported kind.
func (h *NodeAPIHandler) GetOperation(
	ctx context.Context,
	request nodeapi.GetOperationRequestObject,
) (nodeapi.GetOperationResponseObject, error) {
	if err := requireNodePrincipal(ctx); err != nil {
		return nil, err
	}
	snapshot, err := h.operationManager.GetByID(request.RequestID)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("get operation: %w", err))
	}
	payload, err := operationResponse(snapshot)
	if err != nil {
		return nil, fmt.Errorf("get operation: %w", err)
	}
	return nodeapi.GetOperation200JSONResponse(payload), nil
}

// StopOperation records an asynchronous stop intent for any supported kind.
func (h *NodeAPIHandler) StopOperation(
	ctx context.Context,
	request nodeapi.StopOperationRequestObject,
) (nodeapi.StopOperationResponseObject, error) {
	if err := requireNodePrincipal(ctx); err != nil {
		return nil, err
	}
	snapshot, initiated, err := h.operationManager.StopByID(request.RequestID)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("stop operation: %w", err))
	}
	payload, err := operationResponse(snapshot)
	if err != nil {
		return nil, fmt.Errorf("stop operation: %w", err)
	}
	if initiated {
		return nodeapi.StopOperation202JSONResponse(payload), nil
	}
	return nodeapi.StopOperation200JSONResponse(payload), nil
}

// NewNodeAPIHandler constructs a generated-protocol adapter.
func NewNodeAPIHandler(
	operationManager *operation.Manager,
	profilingService profilingOperationService,
	tracingService tracingOperationService,
) (*NodeAPIHandler, error) {
	if operationManager == nil {
		return nil, errors.New("create Node API handler: operation manager is required")
	}
	if profilingService == nil {
		return nil, errors.New("create Node API handler: profiling service is required")
	}
	if tracingService == nil {
		return nil, errors.New("create Node API handler: tracing service is required")
	}
	var specification map[string]any
	if err := json.Unmarshal(nodeapi.OpenAPIJSON(), &specification); err != nil {
		return nil, fmt.Errorf("create Node API handler: decode bundled OpenAPI: %w", err)
	}
	return &NodeAPIHandler{
		operationManager: operationManager,
		profiling:        profilingService,
		tracing:          tracingService,
		openAPI:          specification,
	}, nil
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

func requireNodePrincipal(ctx context.Context) error {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok || principal.ID != nodePrincipalID {
		return response.NewAPIError(apiv1.ErrorCodeUnauthenticated, "valid service credentials are required")
	}
	return nil
}

func profilingOperationRequest(
	body *nodeapi.StartOperationJSONRequestBody,
	spec nodeapi.ProfilingOperationSpec,
) (nodeprofiling.StartRequest, error) {
	duration, err := secondsDuration(body.DurationSeconds)
	if err != nil {
		return nodeprofiling.StartRequest{}, fmt.Errorf("%w: %w", nodeprofiling.ErrInvalidRequest, err)
	}
	return nodeprofiling.StartRequest{
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
	}, nil
}

func tracingOperationRequest(
	body *nodeapi.StartOperationJSONRequestBody,
	spec nodeapi.TracingOperationSpec,
) (nodetracing.StartRequest, error) {
	duration, err := secondsDuration(body.DurationSeconds)
	if err != nil {
		return nodetracing.StartRequest{}, fmt.Errorf("%w: %w", nodetracing.ErrInvalidRequest, err)
	}
	return nodetracing.StartRequest{
		RequestID:   body.RequestID,
		Duration:    duration,
		Scope:       observation.Scope(body.Scope),
		ContainerID: optionalString(body.ContainerID),
		Spec: tracingdomain.Spec{
			Type: tracingdomain.Type(spec.Type),
		},
	}, nil
}

func secondsDuration(seconds int64) (time.Duration, error) {
	if seconds <= 0 {
		return 0, errors.New("duration seconds must be greater than zero")
	}
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

func operationResponse(snapshot *operation.Operation) (nodeapi.OperationResponse, error) {
	if snapshot == nil {
		return nodeapi.OperationResponse{}, errors.New("operation snapshot is nil")
	}
	status, err := operationStatus(snapshot.Status)
	if err != nil {
		return nodeapi.OperationResponse{}, err
	}
	payload := nodeapi.Operation{
		RequestID:  snapshot.RequestID,
		Kind:       nodeapi.OperationKind(snapshot.Kind),
		Status:     status,
		CreatedAt:  snapshot.CreatedAt,
		StartedAt:  snapshot.StartedAt,
		FinishedAt: snapshot.FinishedAt,
	}
	if snapshot.Failure != nil {
		code, err := operationFailureCode(snapshot.Failure.Reason)
		if err != nil {
			return nodeapi.OperationResponse{}, err
		}
		payload.Failure = &nodeapi.OperationFailure{
			Code:    code,
			Message: snapshot.Failure.Message,
		}
	}
	return nodeapi.OperationResponse{Data: payload}, nil
}

func operationStatus(status operation.Status) (nodeapi.OperationStatus, error) {
	switch status {
	case operation.StatusPending:
		return nodeapi.OperationStatusPending, nil
	case operation.StatusRunning:
		return nodeapi.OperationStatusRunning, nil
	case operation.StatusStopping:
		return nodeapi.OperationStatusStopping, nil
	case operation.StatusCompleted:
		return nodeapi.OperationStatusCompleted, nil
	case operation.StatusFailed:
		return nodeapi.OperationStatusFailed, nil
	case operation.StatusStopped:
		return nodeapi.OperationStatusStopped, nil
	default:
		return "", fmt.Errorf("unsupported operation status %q", status)
	}
}

func operationFailureCode(reason operation.FailureReason) (apiv1.ErrorCode, error) {
	switch reason {
	case operation.FailureReasonExecutionStartFailed:
		return nodeapi.ErrorCodeExecutionStartFailed, nil
	case operation.FailureReasonLaunchTimeout:
		return nodeapi.ErrorCodeLaunchTimeout, nil
	case operation.FailureReasonExecutionFailed:
		return nodeapi.ErrorCodeExecutionFailed, nil
	case operation.FailureReasonExecutionStopFailed:
		return nodeapi.ErrorCodeExecutionStopFailed, nil
	case operation.FailureReasonFinalizationFailed:
		return nodeapi.ErrorCodeFinalizationFailed, nil
	case operation.FailureReasonFinalizationTimeout:
		return nodeapi.ErrorCodeFinalizationTimeout, nil
	default:
		return "", fmt.Errorf("unsupported operation failure reason %q", reason)
	}
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
