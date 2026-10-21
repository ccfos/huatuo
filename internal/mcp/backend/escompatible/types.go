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

// Package escompatible provides MCP's bounded, read-only Elasticsearch and
// OpenSearch event reader.
package escompatible

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ccfos/huatuo/internal/mcp/constant"
)

// ResponseError preserves HTTP status for MCP retry/error classification.
type ResponseError struct {
	StatusCode int
	Body       string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("Elasticsearch returned HTTP %d: %s", e.StatusCode, e.Body)
}

func (e *ResponseError) HTTPStatusCode() int { return e.StatusCode }

// DiagnosticSummary exposes bounded backend details without logging request headers.
func (e *ResponseError) DiagnosticSummary() string {
	return boundedDiagnosticValue(e.Error(), constant.MaxBackendErrorBodyBytes)
}

func boundedDiagnosticValue(value string, maximum int) string {
	if len(value) > maximum {
		value = value[:maximum] + "... (truncated)"
	}
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.ReplaceAll(value, "\r", " ")
}

func (e *ResponseError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}
