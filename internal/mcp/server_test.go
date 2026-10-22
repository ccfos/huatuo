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

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tracingstore "github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/mcp/tool"

	"github.com/prometheus/client_golang/prometheus"
)

type testEventStore struct{}

func (testEventStore) BackendType() string { return constant.EventBackendElasticsearch }

func (testEventStore) SearchEvents(
	context.Context,
	*tracingstore.Query,
) (tracingstore.SearchResult, error) {
	return tracingstore.SearchResult{}, nil
}

func (testEventStore) Count(context.Context, *tracingstore.Query) (int64, error) {
	return 0, nil
}

func (testEventStore) TracerNames(context.Context, *tracingstore.Query) ([]string, error) {
	return nil, nil
}

func TestServerInitializeAndListKnowledge(t *testing.T) {
	server := newTestServer(t, &Config{MaxConcurrentRequests: 2})

	initialize := call(t, server, `{
		"jsonrpc":"2.0","id":1,"method":"initialize","params":{
			"protocolVersion":"2025-03-26","capabilities":{},
			"clientInfo":{"name":"test","version":"1"}
		}}`)
	if initialize.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, body = %s", initialize.Code, initialize.Body.String())
	}

	resources := call(t, server, `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`)
	if resources.Code != http.StatusOK {
		t.Fatalf("resources/list status = %d, body = %s", resources.Code, resources.Body.String())
	}
	var response struct {
		Result struct {
			Resources []struct {
				URI string `json:"uri"`
			} `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resources.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode resources/list response: %v", err)
	}
	foundStorageModel := false
	foundProfilingMethodology := false
	for _, resource := range response.Result.Resources {
		switch resource.URI {
		case "huatuo://storage-model":
			foundStorageModel = true
		case "huatuo://profiling/methodology":
			foundProfilingMethodology = true
		}
	}
	if !foundStorageModel || !foundProfilingMethodology {
		t.Fatalf("resources = %+v", response.Result.Resources)
	}
}

func TestServerExportsTransportMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := newTestServer(t, &Config{MaxConcurrentRequests: 1, Registerer: registry})
	response := call(t, server, `{
		"jsonrpc":"2.0","id":1,"method":"initialize","params":{
			"protocolVersion":"2025-03-26","capabilities":{},
			"clientInfo":{"name":"test","version":"1"}
		}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("initialize status = %d", response.Code)
	}
	for _, request := range []struct {
		method string
		body   string
	}{
		{method: "resources/list", body: `{"jsonrpc":"2.0","id":2,"method":"resources/list"}`},
		{method: "prompts/list", body: `{"jsonrpc":"2.0","id":3,"method":"prompts/list"}`},
		{method: "tools/list", body: `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`},
	} {
		response = call(t, server, request.body)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", request.method, response.Code)
		}
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	wanted := map[string]bool{
		"huatuo_apiserver_mcp_active_requests":              false,
		"huatuo_apiserver_mcp_concurrency_rejections_total": false,
		"huatuo_apiserver_mcp_concurrency_limit":            false,
		"huatuo_apiserver_mcp_operations_total":             false,
	}
	for _, family := range families {
		if _, ok := wanted[family.GetName()]; ok {
			wanted[family.GetName()] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("metric %q was not gathered", name)
		}
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_operations_total", map[string]string{
		"method": "initialize", "outcome": "success",
	}); got != 1 {
		t.Fatalf("initialize operations = %v, want 1", got)
	}
	for _, method := range []string{"resources/list", "prompts/list", "tools/list"} {
		if got := metricValue(t, registry, "huatuo_apiserver_mcp_operations_total", map[string]string{
			"method": method, "outcome": "success",
		}); got != 1 {
			t.Fatalf("%s operations = %v, want 1", method, got)
		}
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_concurrency_limit", map[string]string{
		"kind": constant.ConcurrencyKindTotal,
	}); got != 1 {
		t.Fatalf("total concurrency limit = %v, want 1", got)
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_active_requests", map[string]string{
		"kind": constant.RequestKindRPC,
	}); got != 0 {
		t.Fatalf("active RPC requests = %v, want 0", got)
	}
}

func TestStreamRequestsCountTowardTotalConcurrency(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := newMetrics(registry, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		concurrent: make(chan struct{}, 1),
		streams:    make(chan struct{}, 1),
		metrics:    metrics,
	}
	server.concurrent <- struct{}{}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/mcp", http.NoBody))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_concurrency_rejections_total", map[string]string{
		"kind": constant.ConcurrencyKindTotal,
	}); got != 1 {
		t.Fatalf("total concurrency rejections = %v, want 1", got)
	}
}

func TestStreamConcurrencyRejectionMetric(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := newMetrics(registry, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		concurrent: make(chan struct{}, 2),
		streams:    make(chan struct{}, 1),
		metrics:    metrics,
	}
	server.streams <- struct{}{}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/mcp", http.NoBody))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_concurrency_rejections_total", map[string]string{
		"kind": constant.ConcurrencyKindStream,
	}); got != 1 {
		t.Fatalf("stream concurrency rejections = %v, want 1", got)
	}
}

func TestActiveStreamMetricTracksLifetime(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := newTestServer(t, &Config{MaxConcurrentRequests: 4, Registerer: registry})
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodGet, "/v1/mcp", http.NoBody).WithContext(ctx)
	request.Header.Set("Accept", "text/event-stream")
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServeHTTP(httptest.NewRecorder(), request)
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if metricValue(t, registry, "huatuo_apiserver_mcp_active_requests", map[string]string{
			"kind": constant.RequestKindStream,
		}) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_active_requests", map[string]string{
		"kind": constant.RequestKindStream,
	}); got != 1 {
		cancel()
		<-done
		t.Fatalf("active stream requests = %v, want 1", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_active_requests", map[string]string{
		"kind": constant.RequestKindStream,
	}); got != 0 {
		t.Fatalf("active stream requests after cancellation = %v, want 0", got)
	}
}

func TestShutdownCancelsActiveStreamAndRejectsNewRequests(t *testing.T) {
	server := newTestServer(t, &Config{MaxConcurrentRequests: 4})
	backendsClosed := false
	server.closeBackends = func(context.Context) error {
		if len(server.streams) != 0 || len(server.concurrent) != 0 {
			t.Error("backends closed before active requests drained")
		}
		backendsClosed = true
		return nil
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("MCP shutdown: %v", err)
	}
	if !backendsClosed {
		t.Fatal("backend cleanup was not called")
	}
	if err := httpServer.Config.Shutdown(ctx); err != nil {
		t.Fatalf("HTTP shutdown: %v", err)
	}
	server.mu.Lock()
	remaining := len(server.cancels)
	server.mu.Unlock()
	if remaining != 0 || len(server.streams) != 0 || len(server.concurrent) != 0 {
		t.Fatalf("shutdown retained requests: %d", remaining)
	}
	recorder := call(t, server, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("new request status = %d", recorder.Code)
	}
}

func TestServerExposesDiagnosticGuideTool(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := newTestServer(t, &Config{MaxConcurrentRequests: 2, Registerer: registry})

	response := call(t, server, fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{}}}`,
		constant.ToolDiagnosticGuide,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("guide status = %d, body = %s", response.Code, response.Body.String())
	}
	guide := decodeGuideResult(t, response)
	if !guide.Resources["huatuo://storage-model"] ||
		guide.Resources["huatuo://cross-domain"] {
		t.Fatalf("overview guide = %+v", guide)
	}

	response = call(t, server, fmt.Sprintf(
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":{"stage":"subsystem","subsystem":"cpu"}}}`,
		constant.ToolDiagnosticGuide,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("subsystem guide status = %d, body = %s", response.Code, response.Body.String())
	}
	guide = decodeGuideResult(t, response)
	if !guide.Resources["huatuo://subsystems/cpu/tracers"] ||
		guide.Resources["huatuo://storage-model"] {
		t.Fatalf("subsystem guide = %+v", guide)
	}

	response = call(t, server, fmt.Sprintf(
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":%q,"arguments":{"stage":"profiler"}}}`,
		constant.ToolDiagnosticGuide,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("profiler guide status = %d, body = %s", response.Code, response.Body.String())
	}
	guide = decodeGuideResult(t, response)
	if !guide.Resources["huatuo://profiling/methodology"] ||
		!guide.Resources["huatuo://profiling/kernel-signatures"] {
		t.Fatalf("profiler guide = %+v", guide)
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_tool_calls_total", map[string]string{
		"tool": constant.ToolDiagnosticGuide, "outcome": "success", "error_class": "none",
	}); got != 3 {
		t.Fatalf("guide tool call metric = %v, want 3", got)
	}
	response = call(t, server, fmt.Sprintf(
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":%q,"arguments":{"stage":"invalid"}}}`,
		constant.ToolDiagnosticGuide,
	))
	if response.Code != http.StatusOK ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"isError":true`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte("stage must be")) {
		t.Fatalf("invalid guide stage response = %s", response.Body.String())
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_operations_total", map[string]string{
		"method": "tools/call", "outcome": "error",
	}); got != 1 {
		t.Fatalf("failed tool operations = %v, want 1", got)
	}
}

func metricValue(t *testing.T, registry *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			matched := true
			for labelName, labelValue := range labels {
				found := false
				for _, label := range metric.Label {
					if label.GetName() == labelName && label.GetValue() == labelValue {
						found = true
						break
					}
				}
				if !found {
					matched = false
					break
				}
			}
			if matched {
				switch {
				case metric.Counter != nil:
					return metric.Counter.GetValue()
				case metric.Gauge != nil:
					return metric.Gauge.GetValue()
				}
			}
		}
	}
	t.Fatalf("metric %q with labels %v not found", name, labels)
	return 0
}

func TestServerRegistersSixToolContract(t *testing.T) {
	server := newTestServer(t, &Config{
		MaxConcurrentRequests: 2,
		MaxRegionsPerQuery:    4,
		Events:                testEventStore{},
		EventConfig: tool.EventConfig{
			MaxDocumentsPerQuery: 37,
		},
		Prometheus: tool.PrometheusConfig{
			Address: "http://prometheus.example:9090",
		},
	})
	response := call(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, body = %s", response.Code, response.Body.String())
	}
	type propertySchema struct {
		Type     string `json:"type"`
		MinItems int    `json:"minItems"`
		MaxItems int    `json:"maxItems"`
	}
	type listedTool struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			Properties map[string]propertySchema `json:"properties"`
			Required   []string                  `json:"required"`
		} `json:"inputSchema"`
	}
	var payload struct {
		Result struct {
			Tools []listedTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	wanted := map[string]bool{
		"diagnostic_guide":           false,
		constant.ToolDiscoverTracers: false,
		constant.ToolQueryEvents:     false,
		constant.ToolQueryProfiles:   false,
		constant.ToolDiscoverMetrics: false,
		constant.ToolQueryMetrics:    false,
	}
	registered := make(map[string]listedTool, len(payload.Result.Tools))
	for _, entry := range payload.Result.Tools {
		for _, rule := range []string{"invalid_argument", "retryable=false", "status=partial", "not an empty result"} {
			if !strings.Contains(entry.Description, rule) {
				t.Errorf("tool %s omits error-handling guidance %q", entry.Name, rule)
			}
		}
		if _, ok := wanted[entry.Name]; !ok {
			t.Errorf("unexpected tool %q", entry.Name)
		} else {
			wanted[entry.Name] = true
		}
		registered[entry.Name] = entry
	}
	if len(payload.Result.Tools) != len(wanted) {
		t.Errorf("registered %d tools, want %d", len(payload.Result.Tools), len(wanted))
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("tool %q is not registered", name)
		}
	}
	requireProperty := func(toolName, property, propertyType string, minItems int) {
		t.Helper()
		schema, ok := registered[toolName].InputSchema.Properties[property]
		if !ok || schema.Type != propertyType || schema.MinItems != minItems {
			t.Errorf("%s.%s schema = %+v", toolName, property, schema)
		}
	}
	requireRequired := func(toolName string, properties ...string) {
		t.Helper()
		required := make(map[string]bool, len(registered[toolName].InputSchema.Required))
		for _, property := range registered[toolName].InputSchema.Required {
			required[property] = true
		}
		for _, property := range properties {
			if !required[property] {
				t.Errorf("%s.%s is not required", toolName, property)
			}
		}
	}
	for name := range wanted {
		if name == constant.ToolDiagnosticGuide {
			for _, field := range []string{constant.ArgumentRegion, constant.ArgumentTimeRange} {
				if _, exists := registered[name].InputSchema.Properties[field]; exists {
					t.Errorf("guide unexpectedly exposes %s", field)
				}
			}
			continue
		}
		requireProperty(name, constant.ArgumentRegion, "array", 1)
		requireProperty(name, constant.ArgumentTimeRange, "string", 0)
		requireRequired(name, constant.ArgumentRegion, constant.ArgumentTimeRange)
	}
	requireProperty(constant.ToolDiscoverTracers, "region", "array", 1)
	requireRequired(constant.ToolDiscoverTracers, "region")
	requireProperty(constant.ToolQueryEvents, "tracer_name", "array", 1)
	requireProperty(constant.ToolQueryEvents, "region", "array", 1)
	requireRequired(constant.ToolQueryEvents, "tracer_name", "region", "time_range")
	if got := registered[constant.ToolQueryEvents].InputSchema.Properties["tracer_name"].MaxItems; got != 100 {
		t.Errorf("%s.tracer_name maxItems = %d, want 100", constant.ToolQueryEvents, got)
	}
	for _, toolName := range []string{constant.ToolQueryEvents, constant.ToolQueryProfiles} {
		for _, property := range []string{"host", "container_host", "container_hostnamespace"} {
			if got := registered[toolName].InputSchema.Properties[property].MaxItems; got != 100 {
				t.Errorf("%s.%s maxItems = %d, want 100", toolName, property, got)
			}
		}
	}
	requireProperty(constant.ToolQueryProfiles, "tracer_name", "string", 0)
	requireProperty(constant.ToolQueryProfiles, constant.ArgumentRecordID, "string", 0)
	requireProperty(constant.ToolQueryProfiles, "region", "array", 1)
	requireRequired(constant.ToolQueryProfiles, "tracer_name", "region", "time_range")
	requireProperty(constant.ToolDiscoverMetrics, "region", "array", 1)
	requireRequired(constant.ToolDiscoverMetrics, "region")
	requireProperty(constant.ToolQueryMetrics, "region", "array", 1)
	requireProperty(constant.ToolQueryMetrics, "host", "string", 0)
	requireProperty(constant.ToolQueryMetrics, "container_host", "string", 0)
	requireRequired(constant.ToolQueryMetrics, "query", "region", "time_range")
	for _, toolName := range []string{
		constant.ToolDiscoverTracers,
		constant.ToolQueryEvents,
		constant.ToolQueryProfiles,
		constant.ToolDiscoverMetrics,
		constant.ToolQueryMetrics,
	} {
		if got := registered[toolName].InputSchema.Properties["region"].MaxItems; got != 4 {
			t.Errorf("%s.region maxItems = %d, want 4", toolName, got)
		}
	}

	defaultServer := newTestServer(t, &Config{
		Events:      testEventStore{},
		Prometheus:  tool.PrometheusConfig{Address: "http://prometheus.example:9090"},
		Registerer:  prometheus.NewRegistry(),
		EventConfig: tool.EventConfig{MaxDocumentsPerQuery: 10},
	})
	response = call(t, defaultServer, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	payload.Result.Tools = nil
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode default tools/list response: %v", err)
	}
	for _, entry := range payload.Result.Tools {
		region, hasRegion := entry.InputSchema.Properties[constant.ArgumentRegion]
		if hasRegion && region.MaxItems != constant.MaxRegions {
			t.Errorf("%s.region default maxItems = %d, want %d", entry.Name, region.MaxItems, constant.MaxRegions)
		}
	}
}

func TestDataToolsRejectMissingOrInvalidScope(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("invalid scope reached Prometheus backend")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer backend.Close()
	server := newTestServer(t, &Config{Events: testEventStore{}, MaxRegionsPerQuery: 2, MaxQueryTimeRange: time.Hour, Prometheus: tool.PrometheusConfig{Address: backend.URL}})
	defer func() { _ = server.Shutdown(t.Context()) }()
	for _, name := range []string{constant.ToolDiscoverTracers, constant.ToolQueryEvents, constant.ToolQueryProfiles, constant.ToolDiscoverMetrics, constant.ToolQueryMetrics} {
		for _, test := range []struct {
			name, field string
			value       any
		}{
			{name: "missing region", field: constant.ArgumentRegion},
			{name: "empty region", field: constant.ArgumentRegion, value: []string{}},
			{name: "wildcard region", field: constant.ArgumentRegion, value: []string{"*"}},
			{name: "scalar region", field: constant.ArgumentRegion, value: "cn"},
			{name: "excessive regions", field: constant.ArgumentRegion, value: []string{"cn", "us", "eu"}},
			{name: "missing time", field: constant.ArgumentTimeRange},
			{name: "invalid time", field: constant.ArgumentTimeRange, value: "invalid"},
			{name: "excessive time", field: constant.ArgumentTimeRange, value: "2h"},
		} {
			t.Run(name+"/"+test.name, func(t *testing.T) {
				arguments := dataToolArguments(name)
				if test.value == nil {
					delete(arguments, test.field)
				} else {
					arguments[test.field] = test.value
				}
				response := callTool(t, server, name, arguments)
				var result struct {
					Result struct {
						IsError bool `json:"isError"`
					} `json:"result"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if !result.Result.IsError || !bytes.Contains(response.Body.Bytes(), []byte(test.field)) {
					t.Fatalf("scope accepted or wrong error: %s", response.Body.String())
				}
			})
		}
	}
}

func TestServerSharesMaximumQueryRangeAcrossTools(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := newTestServer(t, &Config{
		MaxConcurrentRequests: 2,
		MaxQueryTimeRange:     time.Hour,
		MaxRegionsPerQuery:    2,
		Events:                testEventStore{},
		EventConfig: tool.EventConfig{
			MaxDocumentsPerQuery: 10,
		},
		Prometheus: tool.PrometheusConfig{Address: "http://prometheus.example:9090"},
		Registerer: registry,
	})
	for _, name := range []string{constant.ToolDiscoverTracers, constant.ToolQueryEvents, constant.ToolQueryProfiles, constant.ToolQueryMetrics, constant.ToolDiscoverMetrics} {
		arguments := dataToolArguments(name)
		arguments[constant.ArgumentTimeRange] = "2h"
		response := callTool(t, server, name, arguments)
		if response.Code != http.StatusOK ||
			!bytes.Contains(response.Body.Bytes(), []byte(`"isError":true`)) ||
			!bytes.Contains(response.Body.Bytes(), []byte("range must not exceed 1h0m0s")) {
			t.Errorf("shared query range response = %s", response.Body.String())
		}
	}
	if got := metricValue(t, registry, "huatuo_apiserver_mcp_tool_calls_total", map[string]string{
		"tool": constant.ToolQueryMetrics, "outcome": "error", "error_class": "invalid_argument",
	}); got != 1 {
		t.Fatalf("query metrics tool calls = %v, want 1", got)
	}
}

type guideResponse struct {
	Resources map[string]bool
}

func decodeGuideResult(t *testing.T, response *httptest.ResponseRecorder) guideResponse {
	t.Helper()
	var protocol struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &protocol); err != nil {
		t.Fatalf("decode tool response: %v", err)
	}
	var payload struct {
		Resources []struct {
			URI string `json:"uri"`
		} `json:"resources"`
	}
	if len(protocol.Result.Content) != 1 {
		t.Fatalf("tool content = %+v", protocol.Result.Content)
	}
	if err := json.Unmarshal([]byte(protocol.Result.Content[0].Text), &payload); err != nil {
		t.Fatalf("decode guide payload: %v", err)
	}
	result := guideResponse{
		Resources: make(map[string]bool, len(payload.Resources)),
	}
	for _, resource := range payload.Resources {
		result.Resources[resource.URI] = true
	}
	return result
}

func dataToolArguments(name string) map[string]any {
	arguments := map[string]any{constant.ArgumentRegion: []string{"cn"}, constant.ArgumentTimeRange: "30m", constant.ArgumentHost: []string{"node-1"}, constant.ArgumentTracerName: []string{"cpusys"}}
	if name == constant.ToolQueryProfiles {
		arguments[constant.ArgumentTracerName] = "cpusys"
	}
	if name == constant.ToolQueryMetrics {
		arguments[constant.ArgumentHost] = "node-1"
		arguments[constant.ArgumentQuery] = `huatuo_cpu{host="node-1"}`
	}
	return arguments
}

func callTool(t *testing.T, server http.Handler, name string, arguments map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
	if err != nil {
		t.Fatal(err)
	}
	return call(t, server, string(payload))
}

func newTestServer(t *testing.T, cfg *Config) *Server {
	t.Helper()
	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func call(t *testing.T, server http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/mcp", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}
