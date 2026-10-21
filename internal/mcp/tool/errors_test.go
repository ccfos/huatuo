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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/escompatible"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestValidationErrorsAreExplicitToolResults(t *testing.T) {
	events := &eventTools{maxRegions: 2, maxQueryTimeRange: time.Hour}
	prom := &prometheusTools{maxRegions: 2, maxQueryRange: time.Hour, maxQuerySize: 10, metricPrefix: "huatuo_"}
	var err error
	prom.regions, err = newRegionRouter(prom, nil)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := NewMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		handler mcpserver.ToolHandlerFunc
		args    map[string]any
		want    string
	}{
		{name: "missing region", handler: events.discoverTracers, args: map[string]any{"time_range": "1m"}, want: "region"},
		{name: "wrong type", handler: events.discoverTracers, args: map[string]any{"region": 3, "time_range": "1m"}, want: "region"},
		{name: "missing time", handler: events.discoverTracers, args: map[string]any{"region": []any{"us01"}}, want: "time_range is required"},
		{name: "invalid time", handler: events.discoverTracers, args: map[string]any{"region": []any{"us01"}, "time_range": "bad"}, want: "invalid time_range"},
		{name: "range limit", handler: events.discoverTracers, args: map[string]any{"region": []any{"us01"}, "time_range": "2h"}, want: "time_range"},
		{name: "PromQL target", handler: prom.query, args: map[string]any{"query": "huatuo_cpu", "region": []any{"us01"}, "host": "node-1", "time_range": "1m"}, want: "every vector selector must contain"},
	} {
		for _, recorder := range []*Metrics{nil, metrics} {
			t.Run(test.name, func(t *testing.T) {
				result, err := recorder.Wrap("test_tool", test.handler)(t.Context(), eventToolRequest(test.args))
				if err != nil || result == nil || !result.IsError {
					t.Fatalf("validation was not returned as a tool error: %+v, %v", result, err)
				}
				var payload toolErrorEnvelope
				text := toolResultErrorText(result)
				if err := json.Unmarshal([]byte(text), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Status != "error" || payload.Tool != "test_tool" || payload.ErrorClass != constant.ToolErrorInvalidArgument || payload.Retryable || payload.Suggestion == "" || !strings.Contains(text, test.want) {
					t.Fatalf("unclear validation result: %s", text)
				}
				if result.StructuredContent == nil {
					t.Fatal("structured error content missing")
				}
			})
		}
	}
}

func TestBackendFailureAndPartialResultsRemainDistinct(t *testing.T) {
	broken := &fakeEventStore{searchErrors: map[string]error{"cpusys": &escompatible.ResponseError{StatusCode: http.StatusForbidden, Body: "read permission denied"}}}
	good := &fakeEventStore{searchDocuments: []*event.Document{{}}}
	router, err := newRegionRouter[event.Reader](broken, []regionValue[event.Reader]{{name: "cn", value: good}})
	if err != nil {
		t.Fatal(err)
	}
	events := &eventTools{stores: router, maxRegions: 2, maxQueryTimeRange: time.Hour, maxDocumentsPerQuery: 10}
	for _, test := range []struct {
		regions []any
		status  string
		isError bool
	}{
		{regions: []any{"us01"}, status: "error", isError: true},
		{regions: []any{"cn", "us01"}, status: "partial"},
	} {
		var recorder *Metrics
		result, err := recorder.Wrap(constant.ToolQueryEvents, events.queryEvents)(t.Context(), eventToolRequest(map[string]any{
			"region": test.regions, "tracer_name": []any{"cpusys"}, "host": []any{"node-1"}, "time_range": "1m",
		}))
		if err != nil || result.IsError != test.isError {
			t.Fatalf("unexpected backend result: %+v, %v", result, err)
		}
		var payload struct {
			Status string          `json:"status"`
			Errors []regionFailure `json:"errors"`
		}
		if err := json.Unmarshal([]byte(textResult(t, result)), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Status != test.status || len(payload.Errors) != 1 {
			t.Fatalf("backend failure confused with empty success: %+v", payload)
		}
		failure := payload.Errors[0]
		if failure.Region != "us01" || failure.Backend != "elasticsearch" || failure.Operation != constant.ToolQueryEvents || failure.ErrorClass != "http_403" || failure.Retryable || !strings.Contains(failure.Error, "read permission denied") || !strings.Contains(failure.Suggestion, "permissions") {
			t.Fatalf("backend error detail missing: %+v", failure)
		}
	}
}

func TestHandlerErrorsAreVisibleToLLM(t *testing.T) {
	var recorder *Metrics
	result, err := recorder.Wrap("test_tool", func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return nil, context.DeadlineExceeded
	})(t.Context(), mcpgo.CallToolRequest{})
	if err != nil || !result.IsError || !strings.Contains(toolResultErrorText(result), `"error_class": "timeout"`) {
		t.Fatalf("handler error escaped tool result: %+v, %v", result, err)
	}
}
