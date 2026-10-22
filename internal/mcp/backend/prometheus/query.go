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

package prometheus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ccfos/huatuo/internal/mcp/constant"
)

type QueryResult struct {
	Response  json.RawMessage
	Count     int
	Truncated bool
	Warnings  []string
}

// BoundResponse retains the original Prometheus envelope and backend warnings,
// adding a local truncation marker without interpreting PromQL policy.
func BoundResponse(data []byte, maximum int) (QueryResult, error) {
	if maximum <= 0 {
		return QueryResult{}, fmt.Errorf("%w: series limit must be positive", ErrInvalidRequest)
	}
	if err := validateAPIResponse(data); err != nil {
		return QueryResult{}, err
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return QueryResult{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	raw, exists := payload["data"]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return QueryResult{}, fmt.Errorf("%w: Prometheus success response is missing data", ErrDecodeFailed)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(raw, &response); err != nil {
		return QueryResult{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	result := QueryResult{Response: data}
	if rawWarnings, exists := payload["warnings"]; exists {
		if err := json.Unmarshal(rawWarnings, &result.Warnings); err != nil {
			return QueryResult{}, fmt.Errorf("%w: warnings: %w", ErrDecodeFailed, err)
		}
	}
	rawResult, exists := response["result"]
	if !exists {
		return result, nil
	}
	var resultType string
	if err := json.Unmarshal(response["resultType"], &resultType); err != nil {
		return QueryResult{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	if resultType != constant.ResultTypeVector && resultType != constant.ResultTypeMatrix {
		result.Count = 1
		return result, nil
	}
	var series []json.RawMessage
	if err := json.Unmarshal(rawResult, &series); err != nil {
		return QueryResult{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	result.Count = min(len(series), maximum)
	if len(series) <= maximum {
		return result, nil
	}
	encoded, err := json.Marshal(series[:maximum])
	if err != nil {
		return QueryResult{}, err
	}
	response["result"] = encoded
	payload["mcp_truncated"] = json.RawMessage(`true`)
	result.Warnings = append(result.Warnings, fmt.Sprintf("MCP series limit reached: returned %d of %d series; narrow query scope", maximum, len(series)))
	payload["warnings"], err = json.Marshal(result.Warnings)
	if err != nil {
		return QueryResult{}, err
	}
	payload["data"], err = json.Marshal(response)
	if err != nil {
		return QueryResult{}, err
	}
	result.Response, err = json.Marshal(payload)
	result.Truncated = true
	return result, err
}

func validateAPIResponse(data []byte) error {
	var response struct {
		Status    string `json:"status"`
		ErrorType string `json:"errorType"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	if response.Status == constant.APIStatusSuccess {
		return nil
	}
	if response.Status == "" {
		return fmt.Errorf("%w: Prometheus response is missing a valid status", ErrDecodeFailed)
	}
	if response.ErrorType == "" {
		response.ErrorType = constant.BackendErrorUnknown
	}
	if response.Error == "" {
		response.Error = "unspecified backend error"
	}
	return fmt.Errorf("%w: Prometheus query failed (%s): %s", ErrDecodeFailed, boundedErrorValue(response.ErrorType, 128), boundedErrorValue(response.Error, 512))
}

func boundedErrorValue(value string, maximum int) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " ")
	if len(value) > maximum {
		return value[:maximum] + "..."
	}
	return value
}
