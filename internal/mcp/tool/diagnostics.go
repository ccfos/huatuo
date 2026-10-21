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

	"github.com/ccfos/huatuo/internal/mcp/constant"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

type backendAttemptError struct {
	Attempt    int    `json:"attempt"`
	ErrorClass string `json:"error_class"`
	Error      string `json:"error"`
	Retryable  bool   `json:"retryable"`
}

type backendOperationDiagnostics struct {
	Backend         string                `json:"backend"`
	Operation       string                `json:"operation"`
	Region          string                `json:"region,omitempty"`
	Recovered       bool                  `json:"recovered"`
	AttemptErrors   []backendAttemptError `json:"attempt_errors"`
	OmittedAttempts int                   `json:"omitted_attempts,omitempty"`
}

func (d *backendOperationDiagnostics) record(attempt int, retryable bool, err error) {
	entry := backendAttemptError{
		Attempt: attempt, ErrorClass: backendErrorClass(err),
		Error: boundedLogValue(err.Error(), constant.MaxBackendErrorReasonBytes), Retryable: retryable,
	}
	if len(d.AttemptErrors) < constant.MaxBackendDiagnosticAttempts {
		d.AttemptErrors = append(d.AttemptErrors, entry)
	} else {
		// Keep the final error even when a custom retry policy exceeds the cap.
		d.AttemptErrors[len(d.AttemptErrors)-1] = entry
		d.OmittedAttempts++
	}
}

func observeBackendDiagnostics(ctx context.Context, diagnostic *backendOperationDiagnostics) {
	observation, ok := ctx.Value(toolObservationContextKey{}).(*toolObservation)
	if !ok || len(diagnostic.AttemptErrors) == 0 {
		return
	}
	observation.backendMu.Lock()
	defer observation.backendMu.Unlock()
	if len(observation.backendDiagnostics) < constant.MaxBackendDiagnosticOperations {
		observation.backendDiagnostics = append(observation.backendDiagnostics, *diagnostic)
	} else {
		observation.backendDiagnostics[len(observation.backendDiagnostics)-1] = *diagnostic
		observation.omittedBackendDiagnostics++
	}
}

func attachBackendDiagnostics(result *mcpgo.CallToolResult, observation *toolObservation) *mcpgo.CallToolResult {
	if result == nil {
		return nil
	}
	observation.backendMu.Lock()
	diagnostics := append([]backendOperationDiagnostics(nil), observation.backendDiagnostics...)
	omitted := observation.omittedBackendDiagnostics
	observation.backendMu.Unlock()
	if len(diagnostics) == 0 {
		return result
	}
	if payload, ok := result.StructuredContent.(*toolErrorEnvelope); ok {
		payload.BackendDiagnostics, payload.OmittedBackendDiagnostics = diagnostics, omitted
		return explicitToolErrorResult(payload)
	}
	// Preserve original numeric tokens, including counters beyond float64 precision.
	payload := map[string]json.RawMessage{}
	object := false
	if len(result.Content) == 1 {
		if text, ok := result.Content[0].(mcpgo.TextContent); ok {
			object = json.Unmarshal([]byte(text.Text), &payload) == nil && payload != nil
		}
	}
	if !object {
		payload = make(map[string]json.RawMessage)
	}
	encodedDiagnostics, err := json.Marshal(diagnostics)
	if err != nil {
		return result
	}
	payload["backend_diagnostics"] = encodedDiagnostics
	if omitted > 0 {
		encodedOmitted, err := json.Marshal(omitted)
		if err != nil {
			return result
		}
		payload["omitted_backend_diagnostics"] = encodedOmitted
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return result
	}
	copyResult := *result
	if object {
		copyResult.Content = mcpgo.NewToolResultText(string(data)).Content
		copyResult.StructuredContent = payload
	} else {
		copyResult.Content = append(append([]mcpgo.Content(nil), result.Content...), mcpgo.NewToolResultText(string(data)).Content...)
	}
	return &copyResult
}
