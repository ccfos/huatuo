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
	Get(requestID string) (*operation.Operation, error)
	Stop(
		requestID string,
	) (operationSnapshot *operation.Operation, initiated bool, err error)
}

type tracingOperationService interface {
	Start(
		ctx context.Context,
		request nodetracing.StartRequest,
	) (operationSnapshot *operation.Operation, created bool, err error)
	Get(requestID string) (*operation.Operation, error)
	Stop(
		requestID string,
	) (operationSnapshot *operation.Operation, initiated bool, err error)
}

// NodeAPIHandler implements the generated Node Agent Strict Server.
type NodeAPIHandler struct {
	profiling profilingOperationService
	tracing   tracingOperationService
	openAPI   nodeapi.GetOpenAPI200JSONResponse
}

// NewNodeAPIHandler constructs a generated-protocol adapter.
func NewNodeAPIHandler(
	profilingService profilingOperationService,
	tracingService tracingOperationService,
) (*NodeAPIHandler, error) {
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
		profiling: profilingService,
		tracing:   tracingService,
		openAPI:   specification,
	}, nil
}

// GetHealth reports only whether the HTTP process can serve a request.
func (*NodeAPIHandler) GetHealth(
	context.Context,
	nodeapi.GetHealthRequestObject,
) (nodeapi.GetHealthResponseObject, error) {
	return nodeapi.GetHealth204Response{}, nil
}

// GetOpenAPI returns the bundled protocol document.
func (h *NodeAPIHandler) GetOpenAPI(
	context.Context,
	nodeapi.GetOpenAPIRequestObject,
) (nodeapi.GetOpenAPIResponseObject, error) {
	return h.openAPI, nil
}

// StartProfiling starts or resolves an idempotent profiling operation.
func (h *NodeAPIHandler) StartProfiling(
	ctx context.Context,
	request nodeapi.StartProfilingRequestObject,
) (nodeapi.StartProfilingResponseObject, error) {
	if err := requireNodePrincipal(ctx); err != nil {
		return nil, err
	}
	command, err := profilingStartRequest(request.Body)
	if err != nil {
		return nil, nodeAPIError(err)
	}
	operationSnapshot, created, err := h.profiling.Start(ctx, &command)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("start profiling: %w", err))
	}
	payload, err := operationResponse(operationSnapshot)
	if err != nil {
		return nil, fmt.Errorf("start profiling: %w", err)
	}
	if created {
		return nodeapi.StartProfiling202JSONResponse(payload), nil
	}
	return nodeapi.StartProfiling200JSONResponse(payload), nil
}

// GetProfiling returns a retained profiling operation.
func (h *NodeAPIHandler) GetProfiling(
	ctx context.Context,
	request nodeapi.GetProfilingRequestObject,
) (nodeapi.GetProfilingResponseObject, error) {
	if err := requireNodePrincipal(ctx); err != nil {
		return nil, err
	}
	operationSnapshot, err := h.profiling.Get(request.RequestID)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("get profiling: %w", err))
	}
	payload, err := operationResponse(operationSnapshot)
	if err != nil {
		return nil, fmt.Errorf("get profiling: %w", err)
	}
	return nodeapi.GetProfiling200JSONResponse(payload), nil
}

// StopProfiling records an asynchronous profiling stop intent.
func (h *NodeAPIHandler) StopProfiling(
	ctx context.Context,
	request nodeapi.StopProfilingRequestObject,
) (nodeapi.StopProfilingResponseObject, error) {
	if err := requireNodePrincipal(ctx); err != nil {
		return nil, err
	}
	operationSnapshot, initiated, err := h.profiling.Stop(request.RequestID)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("stop profiling: %w", err))
	}
	payload, err := operationResponse(operationSnapshot)
	if err != nil {
		return nil, fmt.Errorf("stop profiling: %w", err)
	}
	if initiated {
		return nodeapi.StopProfiling202JSONResponse(payload), nil
	}
	return nodeapi.StopProfiling200JSONResponse(payload), nil
}

// StartTracing validates the protocol and reports executor availability.
func (h *NodeAPIHandler) StartTracing(
	ctx context.Context,
	request nodeapi.StartTracingRequestObject,
) (nodeapi.StartTracingResponseObject, error) {
	if err := requireNodePrincipal(ctx); err != nil {
		return nil, err
	}
	command, err := tracingStartRequest(request.Body)
	if err != nil {
		return nil, nodeAPIError(err)
	}
	operationSnapshot, created, err := h.tracing.Start(ctx, command)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("start tracing: %w", err))
	}
	payload, err := operationResponse(operationSnapshot)
	if err != nil {
		return nil, fmt.Errorf("start tracing: %w", err)
	}
	if created {
		return nodeapi.StartTracing202JSONResponse(payload), nil
	}
	return nodeapi.StartTracing200JSONResponse(payload), nil
}

// GetTracing returns a retained tracing operation.
func (h *NodeAPIHandler) GetTracing(
	ctx context.Context,
	request nodeapi.GetTracingRequestObject,
) (nodeapi.GetTracingResponseObject, error) {
	if err := requireNodePrincipal(ctx); err != nil {
		return nil, err
	}
	operationSnapshot, err := h.tracing.Get(request.RequestID)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("get tracing: %w", err))
	}
	payload, err := operationResponse(operationSnapshot)
	if err != nil {
		return nil, fmt.Errorf("get tracing: %w", err)
	}
	return nodeapi.GetTracing200JSONResponse(payload), nil
}

// StopTracing records an asynchronous tracing stop intent.
func (h *NodeAPIHandler) StopTracing(
	ctx context.Context,
	request nodeapi.StopTracingRequestObject,
) (nodeapi.StopTracingResponseObject, error) {
	if err := requireNodePrincipal(ctx); err != nil {
		return nil, err
	}
	operationSnapshot, initiated, err := h.tracing.Stop(request.RequestID)
	if err != nil {
		return nil, nodeAPIError(fmt.Errorf("stop tracing: %w", err))
	}
	payload, err := operationResponse(operationSnapshot)
	if err != nil {
		return nil, fmt.Errorf("stop tracing: %w", err)
	}
	if initiated {
		return nodeapi.StopTracing202JSONResponse(payload), nil
	}
	return nodeapi.StopTracing200JSONResponse(payload), nil
}

func requireNodePrincipal(ctx context.Context) error {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok || principal.ID != nodePrincipalID {
		return response.NewAPIError(apiv1.ErrorCodeUnauthenticated, "valid service credentials are required")
	}
	return nil
}

func profilingStartRequest(body *nodeapi.StartProfilingJSONRequestBody) (nodeprofiling.StartRequest, error) {
	if body == nil {
		return nodeprofiling.StartRequest{}, fmt.Errorf("%w: request body is required", nodeprofiling.ErrInvalidRequest)
	}
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
			Type:            profilingdomain.Type(body.Type),
			Language:        profilingdomain.Language(body.Language),
			Mode:            profilingdomain.Mode(body.Mode),
			BinaryMatchPath: optionalString(body.BinaryMatchPath),
		},
	}, nil
}

func tracingStartRequest(body *nodeapi.StartTracingJSONRequestBody) (nodetracing.StartRequest, error) {
	if body == nil {
		return nodetracing.StartRequest{}, fmt.Errorf("%w: request body is required", nodetracing.ErrInvalidRequest)
	}
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
			Type: tracingdomain.Type(body.Type),
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
