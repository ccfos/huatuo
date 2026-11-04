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
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestRetryDiagnosticsReachClientOnRecoveryAndFailure(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(fmt.Sprint(recovered), func(t *testing.T) {
			var recorder *Metrics
			result, err := recorder.Wrap("test_tool", func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				attempts := 0
				_, err := retryBackend(ctx, "elasticsearch", "search_events", "", retryPolicy{
					MaxRetries: 2, Backoff: func(int, time.Duration) time.Duration { return 0 },
				}, nil, "count", func(int) int { return 1 }, func(context.Context) (int, bool, error) {
					attempts++
					if recovered && attempts == 3 {
						return 1, false, nil
					}
					return 0, true, fmt.Errorf("backend cause at attempt %d", attempts)
				})
				if err != nil {
					return nil, err
				}
				return indentedJSONResult(map[string]any{"status": "success", "events": []string{"kept"}, "counter": int64(9007199254740993)})
			})(t.Context(), mcpgo.CallToolRequest{})
			if err != nil || result.IsError == recovered {
				t.Fatalf("retry result=%+v, error=%v", result, err)
			}
			var payload struct {
				Status      string                        `json:"status"`
				Events      []string                      `json:"events"`
				Diagnostics []backendOperationDiagnostics `json:"backend_diagnostics"`
			}
			if err := json.Unmarshal([]byte(textResult(t, result)), &payload); err != nil {
				t.Fatal(err)
			}
			wantAttempts := 3
			if recovered {
				wantAttempts = 2
				if payload.Status != "success" || len(payload.Events) != 1 || payload.Events[0] != "kept" {
					t.Fatalf("recovered errors corrupted complete data: %+v", payload)
				}
				structured, err := json.Marshal(result.StructuredContent)
				if err != nil || !strings.Contains(textResult(t, result), "9007199254740993") || !strings.Contains(string(structured), "9007199254740993") {
					t.Fatalf("attaching diagnostics changed counter precision: %s, %v", structured, err)
				}
			}
			if len(payload.Diagnostics) != 1 || payload.Diagnostics[0].Recovered != recovered || len(payload.Diagnostics[0].AttemptErrors) != wantAttempts {
				t.Fatalf("retry history lost: %+v", payload)
			}
			for index, attempt := range payload.Diagnostics[0].AttemptErrors {
				if attempt.Attempt != index+1 || !strings.Contains(attempt.Error, fmt.Sprint(index+1)) {
					t.Fatalf("attempt cause lost: %+v", attempt)
				}
			}
		})
	}
}

func TestParallelRegionRetriesKeepDiagnosticScope(t *testing.T) {
	var recorder *Metrics
	result, err := recorder.Wrap("test_tool", func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		outcomes := fanOutRegions(ctx, []string{"a", "b", "c", "d"}, 4, func(ctx context.Context, region string) (string, error) {
			attempts := 0
			return retryBackend(ctx, "elasticsearch", "search_events", "", retryPolicy{
				MaxRetries: 1, Backoff: func(int, time.Duration) time.Duration { return 0 },
			}, nil, "count", func(string) int { return 1 }, func(context.Context) (string, bool, error) {
				attempts++
				if attempts == 1 {
					return "", true, errors.New("transient failure in " + region)
				}
				return region, false, nil
			})
		})
		return indentedJSONResult(map[string]any{"status": "success", "results": outcomes})
	})(t.Context(), mcpgo.CallToolRequest{})
	if err != nil || result.IsError {
		t.Fatalf("parallel retry failed: %+v, %v", result, err)
	}
	var payload struct {
		Diagnostics []backendOperationDiagnostics `json:"backend_diagnostics"`
	}
	if err := json.Unmarshal([]byte(textResult(t, result)), &payload); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for index := range payload.Diagnostics {
		diagnostic := &payload.Diagnostics[index]
		if !diagnostic.Recovered || len(diagnostic.AttemptErrors) != 1 || !strings.Contains(diagnostic.AttemptErrors[0].Error, "in "+diagnostic.Region) {
			t.Fatalf("region/cause attribution lost: %+v", diagnostic)
		}
		seen[diagnostic.Region] = true
	}
	if len(seen) != 4 {
		t.Fatalf("parallel diagnostics lost: %+v", payload)
	}
}

func TestDiagnosticBoundsPreserveFinalAttemptAndOmissionCounts(t *testing.T) {
	observation := &toolObservation{}
	ctx := context.WithValue(t.Context(), toolObservationContextKey{}, observation)
	for operation := range MaxBackendDiagnosticOperations + 2 {
		diagnostic := backendOperationDiagnostics{Backend: "elasticsearch", Operation: fmt.Sprint(operation)}
		for attempt := range MaxBackendDiagnosticAttempts + 3 {
			diagnostic.record(attempt+1, true, errors.New(strings.Repeat("x", 10000)))
		}
		observeBackendDiagnostics(ctx, &diagnostic)
	}
	result := attachBackendDiagnostics(mcpgo.NewToolResultText(`{"status":"success"}`), observation)
	var payload struct {
		Diagnostics []backendOperationDiagnostics `json:"backend_diagnostics"`
		Omitted     int                           `json:"omitted_backend_diagnostics"`
	}
	if err := json.Unmarshal([]byte(textResult(t, result)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Diagnostics) != MaxBackendDiagnosticOperations || payload.Omitted != 2 {
		t.Fatalf("operation bound lost: %+v", payload)
	}
	last := payload.Diagnostics[len(payload.Diagnostics)-1]
	if len(last.AttemptErrors) != MaxBackendDiagnosticAttempts || last.OmittedAttempts != 3 || last.AttemptErrors[len(last.AttemptErrors)-1].Attempt != MaxBackendDiagnosticAttempts+3 || !strings.HasSuffix(last.AttemptErrors[0].Error, "...") {
		t.Fatalf("final attempt or truncation marker lost: %+v", last)
	}
}
