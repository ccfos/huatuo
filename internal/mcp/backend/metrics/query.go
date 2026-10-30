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

package metrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	mcpevent "github.com/ccfos/huatuo/internal/mcp/backend/event"
)

const (
	ResultTypeVector = "vector"
	ResultTypeMatrix = "matrix"
	ResultTypeScalar = "scalar"
	ResultTypeString = "string"
)

type QueryResult struct {
	Response  json.RawMessage
	Count     int
	Truncated bool
	Warnings  []string
}

// StatisticsResponse rejects overflow instead of publishing a truncated inventory
// that callers might mistake for complete statistics.
func StatisticsResponse(data []byte, maximum int) (QueryResult, error) {
	result, err := BoundResponse(data, maximum)
	if err != nil {
		return QueryResult{}, err
	}
	if result.Truncated {
		return QueryResult{}, fmt.Errorf("%w: statistics exceed %d output series; reduce grouping dimensions or use count/topk", ErrInvalidRequest, maximum)
	}
	var payload struct {
		Data struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return QueryResult{}, fmt.Errorf("%w: statistics: %w", ErrDecodeFailed, err)
	}
	if len(payload.Data.Result) == 0 || bytes.Equal(bytes.TrimSpace(payload.Data.Result), []byte("null")) {
		return QueryResult{}, fmt.Errorf("%w: instant statistics response is missing result", ErrDecodeFailed)
	}
	switch payload.Data.ResultType {
	case ResultTypeVector:
		var vector []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		}
		if json.Unmarshal(payload.Data.Result, &vector) != nil {
			return QueryResult{}, fmt.Errorf("%w: invalid instant vector", ErrDecodeFailed)
		}
		for index := range vector {
			if vector[index].Metric == nil || !validInstantSample(vector[index].Value) {
				return QueryResult{}, fmt.Errorf("%w: instant vector requires labels and timestamp/value pairs", ErrDecodeFailed)
			}
		}
	case ResultTypeScalar:
		var sample []json.RawMessage
		if json.Unmarshal(payload.Data.Result, &sample) != nil || !validInstantSample(sample) {
			return QueryResult{}, fmt.Errorf("%w: invalid instant scalar", ErrDecodeFailed)
		}
	default:
		return QueryResult{}, fmt.Errorf("%w: statistics require an instant vector or scalar", ErrDecodeFailed)
	}
	return result, nil
}

func validInstantSample(sample []json.RawMessage) bool {
	value, ok := instantSampleValue(sample)
	if !ok {
		return false
	}
	_, err := strconv.ParseFloat(value, 64)
	return err == nil
}

func instantSampleValue(sample []json.RawMessage) (string, bool) {
	if len(sample) != 2 {
		return "", false
	}
	var timestamp float64
	var value string
	if len(sample[0]) == 0 || bytes.Equal(bytes.TrimSpace(sample[0]), []byte("null")) || json.Unmarshal(sample[0], &timestamp) != nil ||
		len(sample[1]) == 0 || sample[1][0] != '"' || json.Unmarshal(sample[1], &value) != nil {
		return "", false
	}
	return value, true
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
	if !exists || bytes.Equal(bytes.TrimSpace(rawResult), []byte("null")) {
		return QueryResult{}, fmt.Errorf("%w: Prometheus query response is missing result", ErrDecodeFailed)
	}
	var resultType string
	if err := json.Unmarshal(response["resultType"], &resultType); err != nil {
		return QueryResult{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	switch resultType {
	case ResultTypeVector, ResultTypeMatrix:
	case ResultTypeScalar, ResultTypeString:
		var sample []json.RawMessage
		if json.Unmarshal(rawResult, &sample) != nil {
			return QueryResult{}, fmt.Errorf("%w: invalid %s result; expected timestamp/value pair", ErrDecodeFailed, resultType)
		}
		var valid bool
		if resultType == ResultTypeScalar {
			valid = validInstantSample(sample)
		} else {
			_, valid = instantSampleValue(sample)
		}
		if !valid {
			return QueryResult{}, fmt.Errorf("%w: invalid %s result; expected timestamp/value pair", ErrDecodeFailed, resultType)
		}
		result.Count = 1
		return result, nil
	default:
		return QueryResult{}, fmt.Errorf("%w: unsupported resultType %q; expected vector, matrix, scalar, or string", ErrDecodeFailed, boundedErrorValue(resultType, 128))
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
	if response.Status == APIStatusSuccess {
		return nil
	}
	if response.Status == "" {
		return fmt.Errorf("%w: Prometheus response is missing a valid status", ErrDecodeFailed)
	}
	if response.ErrorType == "" {
		response.ErrorType = mcpevent.BackendErrorUnknown
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
