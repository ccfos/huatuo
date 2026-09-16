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

	apiv1 "github.com/ccfos/huatuo/apis/v1"
	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	nodecloudevents "github.com/ccfos/huatuo/internal/nodeagent/cloudevents"
	"github.com/ccfos/huatuo/internal/nodeagent/operation"
	nodeprofiling "github.com/ccfos/huatuo/internal/nodeagent/profiling"
	nodetracing "github.com/ccfos/huatuo/internal/nodeagent/tracing"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/server/response"
	"github.com/ccfos/huatuo/pkg/observation"
	profilingdomain "github.com/ccfos/huatuo/pkg/profiling"
	tracingdomain "github.com/ccfos/huatuo/pkg/tracing"
)

// NodeAPIHandler implements the generated Node Agent Strict Server.
type NodeAPIHandler struct {
	operationManager  *operation.Manager
	profiling         *nodeprofiling.Service
	tracing           *nodetracing.Service
	cloudEvents       *nodecloudevents.Service
	keepAliveInterval time.Duration
	updateConfig      func(map[string]any) error
	containerByID     func(string) (*pod.Container, error)
	openAPI           nodeapi.GetOpenAPI200JSONResponse
}

// NodeAPIHandlerOptions groups the services required by the Node API adapter.
type NodeAPIHandlerOptions struct {
	OperationManager             *operation.Manager
	ProfilingService             *nodeprofiling.Service
	TracingService               *nodetracing.Service
	CloudEventsService           *nodecloudevents.Service
	EventStreamKeepAliveInterval time.Duration
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
func NewNodeAPIHandler(options *NodeAPIHandlerOptions) (*NodeAPIHandler, error) {
	if err := validateNodeAPIHandlerOptions(options); err != nil {
		return nil, err
	}
	var specification map[string]any
	if err := json.Unmarshal(nodeapi.OpenAPIJSON(), &specification); err != nil {
		return nil, fmt.Errorf("create Node API handler: decode bundled OpenAPI: %w", err)
	}
	return &NodeAPIHandler{
		operationManager:  options.OperationManager,
		profiling:         options.ProfilingService,
		tracing:           options.TracingService,
		cloudEvents:       options.CloudEventsService,
		keepAliveInterval: options.EventStreamKeepAliveInterval,
		updateConfig:      config.UpdateAndSync,
		containerByID:     pod.ContainerByID,
		openAPI:           specification,
	}, nil
}

func validateNodeAPIHandlerOptions(options *NodeAPIHandlerOptions) error {
	if options == nil {
		return errors.New("create Node API handler: options are required")
	}
	if options.OperationManager == nil {
		return errors.New("create Node API handler: operation manager is required")
	}
	if options.ProfilingService == nil {
		return errors.New("create Node API handler: profiling service is required")
	}
	if options.TracingService == nil {
		return errors.New("create Node API handler: tracing service is required")
	}
	if options.CloudEventsService == nil {
		return errors.New("create Node API handler: cloud events service is required")
	}
	if options.EventStreamKeepAliveInterval <= 0 {
		return errors.New("create Node API handler: event stream keepalive interval must be positive")
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
	case errors.Is(err, nodecloudevents.ErrInvalidFilters),
		errors.Is(err, nodeprofiling.ErrInvalidRequest),
		errors.Is(err, nodetracing.ErrInvalidRequest),
		errors.Is(err, operation.ErrInvalidRequest):
		return response.NewAPIError(apiv1.ErrorCodeInvalidRequest, err.Error())
	case errors.Is(err, nodecloudevents.ErrLimitExceeded):
		return response.NewAPIError(nodeapi.ErrorCodeEventStreamLimitExceeded, "event stream capacity is exhausted")
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
