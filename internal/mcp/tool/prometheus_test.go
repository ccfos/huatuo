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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/ccfos/huatuo/internal/httpserver/auth"
	mcpevent "github.com/ccfos/huatuo/internal/mcp/backend/event"
	prombackend "github.com/ccfos/huatuo/internal/mcp/backend/metrics"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

func TestScopePromQL(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr bool
	}{
		{
			name:  "bounded selector",
			query: `rate(huatuo_cpu_seconds_total{host="node-1"}[5m])`,
		},
		{
			name:    "mixed metric prefixes",
			query:   `huatuo_cpu{host="node-1"} + up{host="node-1"}`,
			wantErr: true,
		},
		{
			name:    "caller region",
			query:   `huatuo_cpu{region="cn",host="node-1"}`,
			wantErr: true,
		},
		{
			name:    "missing target",
			query:   `huatuo_cpu`,
			wantErr: true,
		},
		{
			name:    "regex target",
			query:   `huatuo_cpu{host=~"node-.*"}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scoped, err := scopePromQL(tt.query, "cn", ArgumentHost, "node-1", "huatuo_")
			if (err != nil) != tt.wantErr {
				t.Fatalf("scopePromQL() = %q, error = %v, wantErr %v", scoped, err, tt.wantErr)
			}
			if err == nil && (!strings.Contains(scoped, `region="cn"`) ||
				!strings.Contains(scoped, `host="node-1"`)) {
				t.Fatalf("scopePromQL() = %q", scoped)
			}
		})
	}
}

func TestScopeRegionStatisticsEnforcesAggregationAndCost(t *testing.T) {
	for _, query := range []string{
		`topk(20, avg by (host) (avg_over_time(huatuo_cpu[5m])))`,
		`count(avg by (host) (huatuo_cpu) > 0.9)`,
		`scalar(sum(huatuo_cpu))`,
	} {
		scoped, err := scopeRegionStatistics(query, "us", "huatuo_", time.Hour)
		if err != nil || !strings.Contains(scoped, `region="us"`) {
			t.Errorf("statistics rejected: %s -> %s, %v", query, scoped, err)
		}
	}
	for _, query := range []string{
		`huatuo_cpu`,
		`sum(huatuo_cpu) + huatuo_cpu`,
		`sum(huatuo_cpu{region="us"})`,
		`sum(up)`,
		`sum({__name__=~"huatuo_.*"})`,
		`sum(rate(huatuo_cpu[2h]))`,
		`sum(huatuo_cpu offset 1h)`,
		`sum(huatuo_cpu @ 123)`,
		`sum(huatuo_cpu) / sum(huatuo_memory)`,
		`sum(huatuo_cpu + on(host) group_left huatuo_memory)`,
		`sum(huatuo_cpu) + sum(huatuo_cpu) + sum(huatuo_cpu) + sum(huatuo_cpu) + sum(huatuo_cpu)`,
		strings.Repeat("abs(", MaxRegionMetricNodes) + `sum(huatuo_cpu)` + strings.Repeat(")", MaxRegionMetricNodes),
	} {
		if _, err := scopeRegionStatistics(query, "us", "huatuo_", time.Hour); err == nil {
			t.Errorf("unsafe statistics accepted: %s", query)
		}
	}
	if _, err := scopeRegionStatistics(`sum({__name__=~".+"})`, "us", "", time.Hour); err == nil {
		t.Error("empty metric prefix must not permit metric-name regex statistics")
	}
	if _, err := scopeRegionStatistics(`sum(rate(huatuo_cpu[5m]))`, "us", "huatuo_", time.Minute); err == nil {
		t.Error("regional statistics ignored the configured query span")
	}
}

func TestQueryMetricsExplicitRegionalStatistics(t *testing.T) {
	var attempts atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost || r.URL.Path != prombackend.PrometheusQueryPath || r.Form.Get(ArgumentTime) != "2026-10-27T08:00:00Z" ||
			!strings.Contains(r.Form.Get(ArgumentQuery), `region="us"`) || r.Form.Get(ArgumentStep) != "" || r.Form.Get("start") != "" || r.Form.Get(MetricTimeoutParameter) == "" {
			t.Errorf("incorrect instant query: %s %s %v", r.Method, r.URL.Path, r.Form)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"host":"node-a"},"value":[1793088000,"0.9"]}]}}`))
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{address: backend.URL, metricPrefix: "huatuo_", maxQuerySize: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.reader.Close(t.Context()) }()
	client.regions = &regionRouter[*prometheusTools]{fallback: client}
	args := map[string]any{ArgumentScope: QueryScopeRegion, utils.ArgumentRegion: []string{"us"}, ArgumentTime: "2026-10-27T16:00:00+08:00", ArgumentQuery: `topk(20,avg by(host)(huatuo_cpu))`}
	result, err := client.query(t.Context(), eventToolRequest(args))
	if err != nil || result.IsError || attempts.Load() != 1 {
		t.Fatalf("regional query: %+v %v calls=%d", result, err, attempts.Load())
	}
	var payload metricStatisticsResponse
	if err := json.Unmarshal([]byte(textResult(t, result)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Purpose != ResultPurposeStatistics || payload.Scope != QueryScopeRegion || payload.UsageHint != StatisticsUsageHint || len(payload.Results) != 1 {
		t.Fatalf("statistics intent or result missing: %+v", payload)
	}
	for _, change := range []map[string]any{
		{ArgumentScope: "unknown"},
		{ArgumentScope: true},
		{ArgumentTime: ""},
		{ArgumentTime: "now"},
		{ArgumentHost: "node-a"},
		{ArgumentTimeRange: "1h"},
		{ArgumentStep: "1m"},
		{utils.ArgumentRegion: []string{"us", "eu"}},
		{ArgumentQuery: `huatuo_cpu`},
	} {
		invalid := make(map[string]any, len(args))
		for key, value := range args {
			invalid[key] = value
		}
		for key, value := range change {
			invalid[key] = value
		}
		result, err = client.query(t.Context(), eventToolRequest(invalid))
		if err != nil || !result.IsError || attempts.Load() != 1 {
			t.Errorf("invalid regional query dispatched: %v, %+v %v", change, result, err)
		}
	}
	delete(args, ArgumentScope)
	result, err = client.query(t.Context(), eventToolRequest(args))
	if err != nil || !result.IsError || attempts.Load() != 1 {
		t.Fatalf("scope omission silently enabled cross-target queries: %+v %v", result, err)
	}
}

func TestRegionalStatisticsRejectsOverflowAndPreservesWarnings(t *testing.T) {
	for _, test := range []struct {
		body    string
		fail    bool
		partial bool
	}{
		{body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]},{"metric":{},"value":[1,"2"]}]}}`, fail: true},
		{body: `{"status":"success","warnings":["partial backend coverage"],"data":{"resultType":"scalar","result":[1,"2"]}}`, partial: true},
	} {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
		client, err := newPrometheusClientWithConfig(&prometheusClientConfig{address: backend.URL, maxQuerySize: 1})
		if err != nil {
			backend.Close()
			t.Fatal(err)
		}
		client.regions = &regionRouter[*prometheusTools]{fallback: client}
		result, err := client.query(t.Context(), eventToolRequest(map[string]any{ArgumentScope: QueryScopeRegion, utils.ArgumentRegion: []string{"us"}, ArgumentTime: "2026-10-27T08:00:00Z", ArgumentQuery: `count(huatuo_cpu)`}))
		if err != nil || result.IsError != test.fail {
			t.Errorf("statistics response: %+v %v", result, err)
		}
		if !test.fail && (!strings.Contains(textResult(t, result), `"status": "partial"`) || !strings.Contains(textResult(t, result), "partial backend coverage")) {
			t.Errorf("statistics warnings lost: %s", textResult(t, result))
		}
		_ = client.reader.Close(t.Context())
		backend.Close()
	}
}

func TestPrometheusRegionsAreIndependentAndFirstIsFallback(t *testing.T) {
	var requests atomic.Int64
	endpoint := func(auth string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if got := r.Header.Get("Authorization"); got != auth {
				t.Errorf("Authorization = %q, want %q", got, auth)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","data":["huatuo_cpu","up"]}`))
		}))
	}
	private, public := endpoint("Bearer secret"), endpoint("")
	defer private.Close()
	defer public.Close()
	protocol := mcpserver.NewMCPServer("test", "1")
	cleanup, err := registerPrometheusTools(protocol, &PrometheusConfig{
		Regions: []PrometheusRegionConfig{
			{NamePattern: "^private$", Address: private.URL, AuthType: "bearer", AuthToken: "secret", MetricPrefix: "huatuo_"},
			{NamePattern: "public", Address: public.URL},
		},
	}, QueryLimits{MaxRegionsPerQuery: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cleanup(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	if requests.Load() != 0 {
		t.Fatal("regional setup probed a backend")
	}
	for _, test := range []struct {
		region string
		count  int
	}{
		{region: "private", count: 1},
		{region: "public", count: 2},
		{region: "unmatched", count: 1},
	} {
		result, err := protocol.GetTool(ToolDiscoverMetrics).Handler(t.Context(), eventToolRequest(map[string]any{
			"region": []any{test.region}, "time_range": "1h",
		}))
		if err != nil || result.IsError {
			t.Fatalf("discover %s: %+v, %v", test.region, result, err)
		}
		var payload struct {
			Metrics []prometheusMetricInfo `json:"metrics"`
		}
		if err := json.Unmarshal([]byte(textResult(t, result)), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Metrics) != test.count {
			t.Errorf("%s metrics = %+v, want %d", test.region, payload.Metrics, test.count)
		}
	}
}

func TestScopePromQLScopesEverySelector(t *testing.T) {
	query := `huatuo_cpu{host="node-1"} + huatuo_memory{host="node-1"}`
	scoped, err := scopePromQL(query, "cn", ArgumentHost, "node-1", "huatuo_")
	if err != nil {
		t.Fatalf("scopePromQL() error = %v", err)
	}
	if strings.Count(scoped, `region="cn"`) != 2 || strings.Count(scoped, `host="node-1"`) != 2 {
		t.Fatalf("scopePromQL() = %q", scoped)
	}
}

func TestValidatePromQLSafetyRejectsTimeModifiers(t *testing.T) {
	for _, query := range []string{
		`huatuo_cpu{region="cn",host="node-1"} offset 30d`,
		`huatuo_cpu{region="cn",host="node-1"} @ 0`,
		`huatuo_cpu{region="cn",host="node-1"} @ start()`,
	} {
		if err := validatePromQLSafety(query, "huatuo_"); err == nil ||
			!strings.Contains(err.Error(), "modifiers") {
			t.Fatalf("validatePromQLSafety(%q) error = %v", query, err)
		}
	}
}

func TestPrometheusRetriesConsumeBackendBudget(t *testing.T) {
	attempts := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{address: backend.URL, limiter: utils.NewRateLimiter(2)})
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{ID: "operator"})
	_, err = client.get(ctx, "/api/v1/query", nil)
	if !errors.Is(err, utils.ErrRateLimitExceeded) {
		t.Fatalf("get() error = %v, want ErrExceeded", err)
	}
	if attempts != 2 {
		t.Fatalf("backend attempts = %d, want 2", attempts)
	}
}

func TestPrometheusTimeoutAppliesPerAttempt(t *testing.T) {
	var attempts atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		time.Sleep(200 * time.Millisecond)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{
		address: backend.URL, timeout: 50 * time.Millisecond,
		maxRetries: 1, maxDuration: time.Second,
	})
	if err != nil {
		t.Fatalf("newPrometheusClient() error = %v", err)
	}
	started := time.Now()
	_, err = client.get(context.Background(), "/api/v1/query", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("get() error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 350*time.Millisecond {
		t.Fatalf("get() elapsed = %v, want two bounded 50ms attempts", elapsed)
	}
	if attempts.Load() != 2 {
		t.Fatalf("backend attempts = %d, want 2", attempts.Load())
	}
}

func TestPrometheusConfiguredLimitsAndCandidateCatalog(t *testing.T) {
	var seriesRequests atomic.Int32
	var backendQuery atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/label/__name__/values":
			if got := request.URL.Query().Get("match[]"); got != "" {
				t.Errorf("metric catalog scanned series: %q", got)
			}
			_, _ = w.Write([]byte(`{"status":"success","data":["metric_c","metric_a","metric_b"]}`))
		case "/api/v1/series":
			seriesRequests.Add(1)
			_, _ = w.Write([]byte(`{"status":"success","data":[{"__name__":"metric_a","region":"cn","host":"node-1"},{"__name__":"metric_b","region":"cn"},{"__name__":"metric_c","region":"cn"}]}`))
		case prombackend.PrometheusMetadataPath:
			_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
		case "/api/v1/query_range":
			if request.Method != http.MethodPost {
				t.Errorf("query method = %s, want POST", request.Method)
			}
			if err := request.ParseForm(); err != nil {
				t.Errorf("ParseForm() error = %v", err)
			}
			backendQuery.Store(request.Form.Get(ArgumentQuery))
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"id":"1"},"value":[1,"1"]},{"metric":{"id":"2"},"value":[1,"2"]},{"metric":{"id":"3"},"value":[1,"3"]}]}}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{
		address: backend.URL, maxQuerySize: 2, maxDiscover: 2,
	})
	if err != nil {
		t.Fatalf("newPrometheusClientWithConfig() error = %v", err)
	}
	client.regions = &regionRouter[*prometheusTools]{fallback: client}

	discoverRequest := eventToolRequest(map[string]any{
		utils.ArgumentRegion: []any{"cn"},
		ArgumentTimeRange:    "1h",
	})
	discovered, err := client.discover(context.Background(), discoverRequest)
	if err != nil || discovered.IsError {
		t.Fatalf("discover() result = %+v, error = %v", discovered, err)
	}
	var catalog struct {
		Metrics []prometheusMetricInfo `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(discovered.Content[0].(mcpgo.TextContent).Text), &catalog); err != nil {
		t.Fatalf("decode discover result: %v", err)
	}
	metrics := catalog.Metrics
	if len(metrics) != 2 || metrics[0].Name != "metric_a" || metrics[1].Name != "metric_b" {
		t.Fatalf("discover metrics = %+v", metrics)
	}
	if seriesRequests.Load() != 0 || metrics[0].Availability != prombackend.MetricAvailabilityUnknown || len(metrics[0].Labels) != 0 {
		t.Fatalf("catalog unexpectedly scanned series: requests=%d metrics=%+v", seriesRequests.Load(), metrics)
	}

	queryRequest := eventToolRequest(map[string]any{
		ArgumentQuery:        `metric_a{host="node-1"}`,
		utils.ArgumentRegion: []any{"cn"},
		ArgumentHost:         "node-1",
		ArgumentTimeRange:    "1h",
	})
	queried, err := client.query(context.Background(), queryRequest)
	if err != nil || queried.IsError {
		t.Fatalf("query() result = %+v, error = %v", queried, err)
	}
	text := queried.Content[0].(mcpgo.TextContent).Text
	if !strings.Contains(text, `"region": "cn"`) ||
		!strings.Contains(text, `"id": "2"`) || strings.Contains(text, `"id": "3"`) {
		t.Fatalf("query result was not capped:\n%s", text)
	}
	scoped, _ := backendQuery.Load().(string)
	if !strings.Contains(scoped, `region="cn"`) || !strings.Contains(scoped, `host="node-1"`) {
		t.Fatalf("backend query = %q", scoped)
	}
}

func TestDiscoverMetricsUsesRequestedTimeRangeForCatalog(t *testing.T) {
	startTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	endTime := startTime.Add(72 * time.Hour)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/label/__name__/values":
			if got := request.URL.Query().Get("match[]"); got != "" {
				t.Errorf("metric discovery unexpectedly scanned series: %q", got)
			}
			if request.URL.Query().Get("start") != startTime.Format(time.RFC3339Nano) || request.URL.Query().Get("end") != endTime.Format(time.RFC3339Nano) {
				t.Errorf("metric discovery time range = %s", request.URL)
			}
			_, _ = w.Write([]byte(`{"status":"success","data":["huatuo_cpu"]}`))
		case "/api/v1/series":
			t.Errorf("candidate catalog unexpectedly scanned series: %s", request.URL)
		case prombackend.PrometheusMetadataPath:
			_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{
		address: backend.URL, metricPrefix: "huatuo_", maxDiscover: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := client.discoverRegionMetrics(t.Context(), "cn", 10, nil, startTime, endTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics.Metrics) != 1 || metrics.Metrics[0].Name != "huatuo_cpu" || metrics.Metrics[0].Availability != prombackend.MetricAvailabilityUnknown {
		t.Fatalf("metric names = %+v", metrics)
	}
}

func TestDiscoveryDecoratesNameRequest(t *testing.T) {
	var requests atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/label/__name__/values":
			if requests.Add(1) == 1 {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"status":"success","data":["huatuo_a","huatuo_b"]}`))
		case "/api/v1/series":
			_, _ = w.Write([]byte(`{"status":"success","data":[{"__name__":"huatuo_a","region":"cn"},{"__name__":"huatuo_b","region":"cn"}]}`))
		case prombackend.PrometheusMetadataPath:
			_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
		default:
			http.NotFound(w, request)
		}
	}))
	defer backend.Close()
	metrics, err := NewMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{
		address: backend.URL, metricPrefix: "huatuo_", maxDiscover: 2,
		maxRetries: 1, maxDuration: time.Second, metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.reader.Close(t.Context()) }()
	client.regions = &regionRouter[*prometheusTools]{fallback: client}
	result, err := metrics.Wrap(ToolDiscoverMetrics, client.discover)(t.Context(), eventToolRequest(map[string]any{utils.ArgumentRegion: []any{"cn"}, ArgumentTimeRange: "1h"}))
	if err != nil || result.IsError || requests.Load() != 2 {
		t.Fatalf("discovery=%+v, %v, catalog attempts=%d", result, err, requests.Load())
	}
	if got := counterValue(t, metrics.backendAttempts.WithLabelValues("prometheus", prombackend.PrometheusMetricNamesPath, "success")); got != 1 {
		t.Fatalf("successful name requests=%v", got)
	}
	if got := counterValue(t, metrics.backendAttempts.WithLabelValues("prometheus", prombackend.PrometheusMetricNamesPath, "retryable_error")); got != 1 {
		t.Fatalf("failed name attempts=%v", got)
	}
	if got := counterValue(t, metrics.retries.WithLabelValues("prometheus", prombackend.PrometheusMetricNamesPath)); got != 1 {
		t.Fatalf("name retries=%v", got)
	}
}

func TestQueryMetricsRequiresRangeAndOneTarget(t *testing.T) {
	client := &prometheusTools{maxRegions: 2}
	base := map[string]any{
		ArgumentQuery:        `huatuo_cpu{host="node-1"}`,
		utils.ArgumentRegion: []any{"cn"},
		ArgumentHost:         "node-1",
	}
	result, err := client.query(t.Context(), eventToolRequest(base))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(mcpgo.TextContent).Text, "time_range is required") {
		t.Fatalf("query() without time range = %+v, %v", result, err)
	}
	base[ArgumentTimeRange] = "1h"
	base[ArgumentContainerHost] = "pod-1"
	result, err = client.query(t.Context(), eventToolRequest(base))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(mcpgo.TextContent).Text, "exactly one") {
		t.Fatalf("query() with two targets = %+v, %v", result, err)
	}
	delete(base, ArgumentContainerHost)
	base[ArgumentHost] = strings.Repeat("x", mcpevent.MaxTargetValueLength+1)
	result, err = client.query(t.Context(), eventToolRequest(base))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(mcpgo.TextContent).Text, "must not exceed") {
		t.Fatalf("query() with oversized target = %+v, %v", result, err)
	}
}

func TestQueryMetricsReportsTruncationAsPartial(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"id":"1"},"values":[]},{"metric":{"id":"2"},"values":[]}]}}`))
	}))
	defer backend.Close()
	metrics, err := NewMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{address: backend.URL, maxQuerySize: 2, maxRegions: 2, metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	client.regions = &regionRouter[*prometheusTools]{fallback: client}
	result, err := metrics.Wrap(ToolQueryMetrics, client.query)(t.Context(), eventToolRequest(map[string]any{
		ArgumentQuery: `huatuo_cpu{host="node-1"}`, utils.ArgumentRegion: []any{"cn", "us"},
		ArgumentHost: "node-1", ArgumentTimeRange: "1h",
	}))
	if err != nil || result.IsError {
		t.Fatalf("query: %+v, %v", result, err)
	}
	text := textResult(t, result)
	if !strings.Contains(text, `"mcp_truncated": true`) || !strings.Contains(text, "series limit reached") {
		t.Fatalf("missing truncation warning: %s", text)
	}
	if got := counterValue(t, metrics.calls.WithLabelValues(ToolQueryMetrics, ToolOutcomePartialSuccess, ToolErrorNone)); got != 1 {
		t.Fatalf("partial calls = %v", got)
	}
}

func TestDiscoverMetricsReportsRegionalCompleteness(t *testing.T) {
	for _, test := range []struct {
		name      string
		maximum   int
		warning   bool
		truncated bool
		returned  int
	}{
		{name: "exact regional quota", maximum: 4, returned: 4},
		{name: "one regional quota truncates", maximum: 3, truncated: true, returned: 3},
		{name: "backend warning", maximum: 4, warning: true, returned: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				payload := map[string]any{"status": "success", "data": []string{"huatuo_b", "huatuo_a"}}
				if request.URL.Path == prombackend.PrometheusSeriesPath {
					series := make([]map[string]string, 0, len(request.URL.Query()[prombackend.PrometheusMatchParameter]))
					for _, matcher := range request.URL.Query()[prombackend.PrometheusMatchParameter] {
						region := "cn"
						if strings.Contains(matcher, `region="us"`) {
							region = "us"
						}
						for _, name := range []string{"huatuo_a", "huatuo_b"} {
							if strings.Contains(matcher, `__name__="`+name+`"`) {
								series = append(series, map[string]string{"__name__": name, "region": region})
							}
						}
					}
					payload["data"] = series
				} else if request.URL.Path == prombackend.PrometheusMetadataPath {
					payload["data"] = map[string]any{}
				} else if test.warning {
					payload["warnings"] = []string{"catalog partial"}
				}
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer backend.Close()
			metrics, err := NewMetrics(nil)
			if err != nil {
				t.Fatal(err)
			}
			client, err := newPrometheusClientWithConfig(&prometheusClientConfig{address: backend.URL, maxDiscover: test.maximum, maxRegions: 2, metrics: metrics})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.reader.Close(t.Context()) }()
			client.regions = &regionRouter[*prometheusTools]{fallback: client}
			result, err := metrics.Wrap(ToolDiscoverMetrics, client.discover)(t.Context(), eventToolRequest(map[string]any{utils.ArgumentRegion: []any{"cn", "us"}, ArgumentTimeRange: "1h"}))
			if err != nil || result.IsError {
				t.Fatalf("discovery: %+v, %v", result, err)
			}
			var response struct {
				Metrics   []prometheusMetricInfo  `json:"metrics"`
				Truncated bool                    `json:"truncated"`
				Status    []metricDiscoveryStatus `json:"query_status"`
			}
			if err := json.Unmarshal([]byte(textResult(t, result)), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Metrics) != test.returned || response.Truncated != test.truncated || len(response.Status) != 2 {
				t.Fatalf("discovery response=%+v", response)
			}
			for index, status := range response.Status {
				wantTruncated := test.truncated && index == 1
				wantReturned := test.maximum / 2
				if index == 0 {
					wantReturned += test.maximum % 2
				}
				if status.Region != []string{"cn", "us"}[index] || status.Returned != wantReturned || status.Truncated != wantTruncated || (len(status.Warnings) > 0) != (test.warning || wantTruncated) {
					t.Fatalf("regional status=%+v", response.Status)
				}
			}
			outcome := ToolOutcomeSuccess
			if test.truncated || test.warning {
				outcome = ToolOutcomePartialSuccess
			}
			if got := counterValue(t, metrics.calls.WithLabelValues(ToolDiscoverMetrics, outcome, ToolErrorNone)); got != 1 {
				t.Fatalf("%s calls=%v", outcome, got)
			}
		})
	}
}

func TestDiscoverMetricsReportsUnknownTargetedMetricAsPartial(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case prombackend.PrometheusMetricNamesPath:
			_, _ = w.Write([]byte(`{"status":"success","data":["huatuo_a","huatuo_b"]}`))
		case prombackend.PrometheusSeriesPath:
			if strings.Contains(request.URL.Query().Get(prombackend.PrometheusMatchParameter), `huatuo_b`) {
				http.Error(w, "unavailable", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"status":"success","data":[{"__name__":"huatuo_a","region":"cn","host":"node"}]}`))
		case prombackend.PrometheusLabelsPath:
			_, _ = w.Write([]byte(`{"status":"success","data":["__name__","host","region"]}`))
		case prombackend.PrometheusMetadataPath:
			_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
		default:
			t.Errorf("unexpected endpoint: %s", request.URL)
		}
	}))
	defer backend.Close()
	metrics, err := NewMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{address: backend.URL, maxDiscover: 2, maxRegions: 1, metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.reader.Close(t.Context()) }()
	client.regions = &regionRouter[*prometheusTools]{fallback: client}
	result, err := metrics.Wrap(ToolDiscoverMetrics, client.discover)(t.Context(), eventToolRequest(map[string]any{utils.ArgumentRegion: []any{"cn"}, ArgumentTimeRange: "1h", ArgumentMetricNames: []any{"huatuo_a", "huatuo_b"}}))
	if err != nil || result.IsError {
		t.Fatalf("partial discovery failed: %+v, %v", result, err)
	}
	var response struct {
		Metrics []prometheusMetricInfo  `json:"metrics"`
		Errors  []regionFailure         `json:"errors"`
		Status  []metricDiscoveryStatus `json:"query_status"`
	}
	if err := json.Unmarshal([]byte(textResult(t, result)), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Metrics) != 2 || response.Metrics[0].Availability != prombackend.MetricAvailabilityPresent || response.Metrics[1].Availability != prombackend.MetricAvailabilityUnknown || len(response.Errors) != 0 || len(response.Status) != 1 || response.Status[0].Returned != 2 || !strings.Contains(strings.Join(response.Status[0].Warnings, " "), "unknown") {
		t.Fatalf("unverified candidate reported missing/complete: %+v", response)
	}
	if got := counterValue(t, metrics.calls.WithLabelValues(ToolDiscoverMetrics, ToolOutcomePartialSuccess, ToolErrorNone)); got != 1 {
		t.Fatalf("partial discovery calls=%v", got)
	}
}

func TestQueryMetricsBackendWarningsArePartial(t *testing.T) {
	for _, test := range []struct {
		name    string
		data    string
		warning bool
	}{
		{name: "complete", data: `{"resultType":"matrix","result":[{"metric":{"host":"node-1"},"values":[]}]}`},
		{name: "matrix warning", data: `{"resultType":"matrix","result":[{"metric":{"host":"node-1"},"values":[]}]}`, warning: true},
		{name: "empty warning", data: `{"resultType":"matrix","result":[]}`, warning: true},
		{name: "scalar warning", data: `{"resultType":"scalar","result":[1700000000,"1"]}`, warning: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				payload := map[string]any{"status": "success", "data": json.RawMessage(test.data)}
				if test.warning {
					payload["warnings"] = []string{"some series omitted"}
				}
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer backend.Close()
			metrics, err := NewMetrics(nil)
			if err != nil {
				t.Fatal(err)
			}
			client, err := newPrometheusClientWithConfig(&prometheusClientConfig{address: backend.URL, maxQuerySize: 10, maxRegions: 1, metrics: metrics})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.reader.Close(t.Context()) }()
			client.regions = &regionRouter[*prometheusTools]{fallback: client}
			result, err := metrics.Wrap(ToolQueryMetrics, client.query)(t.Context(), eventToolRequest(map[string]any{
				utils.ArgumentRegion: []any{"cn"}, ArgumentQuery: `huatuo_cpu{host="node-1"}`,
				ArgumentHost: "node-1", ArgumentTimeRange: "1h",
			}))
			if err != nil || result.IsError {
				t.Fatalf("query: %+v, %v", result, err)
			}
			if strings.Contains(textResult(t, result), "some series omitted") != test.warning || strings.Contains(textResult(t, result), "mcp_truncated") {
				t.Fatalf("warning response=%s", textResult(t, result))
			}
			outcome := ToolOutcomeSuccess
			if test.warning {
				outcome = ToolOutcomePartialSuccess
			}
			if got := counterValue(t, metrics.calls.WithLabelValues(ToolQueryMetrics, outcome, ToolErrorNone)); got != 1 {
				t.Fatalf("%s calls=%v", outcome, got)
			}
		})
	}
}

func TestQueryMetricsRejectsPrometheusAPIError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorType":"execution","error":"query timed out"}`))
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{
		address: backend.URL, maxQuerySize: 10, maxRegions: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.regions = &regionRouter[*prometheusTools]{fallback: client}
	result, err := client.query(t.Context(), eventToolRequest(map[string]any{
		ArgumentQuery:        `huatuo_cpu{host="node-1"}`,
		utils.ArgumentRegion: []any{"cn"},
		ArgumentHost:         "node-1",
		ArgumentTimeRange:    "1h",
	}))
	if err != nil || !result.IsError {
		t.Fatalf("query() = %+v, %v", result, err)
	}
	text := result.Content[0].(mcpgo.TextContent).Text
	if !strings.Contains(text, "Prometheus query failed (execution): query timed out") {
		t.Fatalf("query error = %s", text)
	}
}

func TestPrometheusConfiguredRetries(t *testing.T) {
	var attempts atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{
		address: backend.URL, maxRetries: 1, maxDuration: time.Second,
	})
	if err != nil {
		t.Fatalf("newPrometheusClientWithConfig() error = %v", err)
	}
	if _, err := client.get(context.Background(), "/api/v1/query", nil); err == nil {
		t.Fatal("get() error = nil")
	}
	if attempts.Load() != 2 {
		t.Fatalf("backend attempts = %d, want 2", attempts.Load())
	}
}

func TestPrometheusConfiguredZeroRetries(t *testing.T) {
	var attempts atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{
		address: backend.URL, maxRetries: 0, maxRetriesSet: true, maxDuration: time.Second,
	})
	if err != nil {
		t.Fatalf("newPrometheusClientWithConfig() error = %v", err)
	}
	if _, err := client.get(context.Background(), "/api/v1/query", nil); err == nil {
		t.Fatal("get() error = nil")
	}
	if attempts.Load() != 1 {
		t.Fatalf("backend attempts = %d, want 1", attempts.Load())
	}
}

func TestPrometheusMaxDurationBoundsRetries(t *testing.T) {
	var attempts atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		time.Sleep(50 * time.Millisecond)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{
		address: backend.URL, maxRetries: 10, maxDuration: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("newPrometheusClientWithConfig() error = %v", err)
	}
	started := time.Now()
	_, err = client.get(context.Background(), "/api/v1/query", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("get() error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("get() elapsed = %v, want retry duration near 20ms", elapsed)
	}
	if attempts.Load() != 1 {
		t.Fatalf("backend attempts = %d, want 1", attempts.Load())
	}
}

func TestValidatePromQLCost(t *testing.T) {
	tests := []string{
		`rate(huatuo_cpu{host="node-1"}[25h])`,
		`sum_over_time(huatuo_cpu{host="node-1"}[1h:1ms])`,
	}
	for _, query := range tests {
		if err := validatePromQLSafety(query, "huatuo_"); err == nil {
			t.Fatalf("validatePromQLSafety(%q) error = nil", query)
		}
	}
}
