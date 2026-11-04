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

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
)

const (
	ToolOutcomeSuccess         = "success"
	ToolErrorInvalidArgument   = "invalid_argument"
	BackendErrorDeadline       = "deadline"
	BackendErrorRateLimit      = "rate_limit"
	BackendErrorTransport      = "transport"
	BackendErrorIncompleteData = "incomplete_data"
	BackendErrorDecode         = "decode"
	BackendErrorInvalidQuery   = "invalid_query"
)

type toolErrorEnvelope struct {
	Status                    string                        `json:"status"`
	Tool                      string                        `json:"tool,omitempty"`
	ErrorClass                string                        `json:"error_class"`
	Message                   string                        `json:"message"`
	Retryable                 bool                          `json:"retryable"`
	Suggestion                string                        `json:"suggestion"`
	Errors                    []regionFailure               `json:"errors,omitempty"`
	QueryStatus               []event.QueryStatus           `json:"query_status,omitempty"`
	BackendDiagnostics        []backendOperationDiagnostics `json:"backend_diagnostics,omitempty"`
	OmittedBackendDiagnostics int                           `json:"omitted_backend_diagnostics,omitempty"`
}

func explicitToolErrorResult(payload *toolErrorEnvelope) *mcpgo.CallToolResult {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return mcpgo.NewToolResultError("failed to encode diagnostic error")
	}
	result := mcpgo.NewToolResultError(string(data))
	result.StructuredContent = payload
	return result
}

func normalizeToolError(name string, result *mcpgo.CallToolResult, observation *toolObservation) *mcpgo.CallToolResult {
	if result == nil || !result.IsError {
		return result
	}
	if payload, ok := result.StructuredContent.(*toolErrorEnvelope); ok {
		payload.Tool = name
		return explicitToolErrorResult(payload)
	}
	class := observation.errorClass
	message := toolResultErrorText(result)
	if class == "" {
		class = ToolErrorInvalidArgument
	}
	if strings.HasPrefix(message, "result exceeds ") {
		class = ToolErrorOversized
	}
	observation.errorClass = class
	suggestion := "Inspect the tool failure details; this is not evidence that the requested data is absent."
	switch class {
	case ToolErrorInvalidArgument:
		suggestion = "Correct the parameter named in message according to tools/list inputSchema, then call the tool again."
	case ToolErrorOversized:
		suggestion = "Reduce the time range, target count or result limit and call the tool again."
	case ToolErrorUnsupportedPayload:
		suggestion = "Use query_events to inspect the source record; the profile payload could not be analyzed."
	}
	return explicitToolErrorResult(&toolErrorEnvelope{
		Status: "error", Tool: name, ErrorClass: class,
		Message: boundedLogValue(message, event.MaxBackendErrorBodyBytes), Suggestion: suggestion,
	})
}

func newRegionFailure(backend, operation, region string, err error) regionFailure {
	class := backendErrorClass(err)
	retryable := retryableEventReaderError(err)
	if backend == BackendPrometheus {
		retryable = retryablePrometheusError(err)
	}
	suggestion := "Inspect the backend error and configuration. A failed query does not establish data absence or a backend outage."
	switch class {
	case "http_401", "http_403":
		suggestion = "Check the MCP backend credentials and read permissions; repeating this request will not fix authorization."
	case "http_404":
		suggestion = "Check the configured index, backend endpoint and proxy route using the original response; a 404 is a query failure, not an empty result."
	case "http_429", BackendErrorRateLimit:
		retryable = true
		suggestion = "Wait before retrying and reduce query frequency."
	case BackendErrorDeadline:
		suggestion = "Reduce the time range or query scope and retry; no complete result was obtained."
	case BackendErrorInvalidQuery:
		class = ToolErrorInvalidArgument
		suggestion = "Correct the query parameters or PromQL using the error detail and tools/list inputSchema, then call again."
	case BackendErrorDecode:
		suggestion = "Check the backend response schema and size; do not interpret a decoding failure as no matching data."
	case BackendErrorIncompleteData:
		suggestion = "Inspect the failed shard reasons and query/date mappings; failed shards do not establish a cluster outage."
	case BackendErrorTransport:
		suggestion = "Check backend connectivity and retry if the connection failure is transient."
	}
	failure := regionFailure{
		Region: region, Backend: backend, Operation: operation, ErrorClass: class, Retryable: retryable,
		Error: boundedLogValue(err.Error(), event.MaxBackendErrorBodyBytes), Suggestion: suggestion,
		metricClass: classifyBackendToolError(err),
	}
	var partial *partialQueryError
	if errors.As(err, &partial) {
		failure.Tracers = partial.FailedTracers
		failure.Causes = partial.Failures
	}
	return failure
}

func collectionStatus(partial bool, failures []regionFailure) string {
	if partial || len(failures) > 0 {
		return ToolResultPartial
	}
	return ToolOutcomeSuccess
}

func backendFailuresResult(ctx context.Context, message string, failures []regionFailure, statuses ...event.QueryStatus) *mcpgo.CallToolResult {
	class := regionFailureErrorClass(failures)
	observeToolErrorClass(ctx, class)
	retryable := len(failures) > 0
	for index := range failures {
		retryable = retryable && failures[index].Retryable
	}
	return explicitToolErrorResult(&toolErrorEnvelope{
		Status: "error", ErrorClass: class, Message: message, Retryable: retryable, Errors: failures,
		QueryStatus: statuses,
		Suggestion:  "The tool query failed, not an empty result. Inspect errors for causes and actions; do not infer a backend outage from a query failure alone.",
	})
}

type backendRetryError struct {
	backend string
	retries int
	err     error
}

func (e *backendRetryError) Error() string {
	return fmt.Sprintf("%s failed after %d retries: %v", e.backend, e.retries, e.err)
}

func (e *backendRetryError) Unwrap() error { return e.err }
