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

	"github.com/ccfos/huatuo/internal/auth"
	"github.com/ccfos/huatuo/internal/mcp/constant"
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
			scoped, err := scopePromQL(tt.query, "cn", constant.ArgumentHost, "node-1", "huatuo_")
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

func TestScopePromQLScopesEverySelector(t *testing.T) {
	query := `huatuo_cpu{host="node-1"} + huatuo_memory{host="node-1"}`
	scoped, err := scopePromQL(query, "cn", constant.ArgumentHost, "node-1", "huatuo_")
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

func TestPrometheusRetriesConsumePrincipalBudget(t *testing.T) {
	attempts := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	client, err := newPrometheusClientWithConfig(&prometheusClientConfig{address: backend.URL, limiter: utils.NewRateLimiter(10, 2)})
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
		case constant.PrometheusMetadataPath:
			_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
		case "/api/v1/query_range":
			if request.Method != http.MethodPost {
				t.Errorf("query method = %s, want POST", request.Method)
			}
			if err := request.ParseForm(); err != nil {
				t.Errorf("ParseForm() error = %v", err)
			}
			backendQuery.Store(request.Form.Get(constant.ArgumentQuery))
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
		constant.ArgumentRegion:    []any{"cn"},
		constant.ArgumentTimeRange: "1h",
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
	if seriesRequests.Load() != 0 || metrics[0].Availability != constant.MetricAvailabilityUnknown || len(metrics[0].Labels) != 0 {
		t.Fatalf("catalog unexpectedly scanned series: requests=%d metrics=%+v", seriesRequests.Load(), metrics)
	}

	queryRequest := eventToolRequest(map[string]any{
		constant.ArgumentQuery:     `metric_a{host="node-1"}`,
		constant.ArgumentRegion:    []any{"cn"},
		constant.ArgumentHost:      "node-1",
		constant.ArgumentTimeRange: "1h",
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
		case constant.PrometheusMetadataPath:
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
	if len(metrics.Metrics) != 1 || metrics.Metrics[0].Name != "huatuo_cpu" || metrics.Metrics[0].Availability != constant.MetricAvailabilityUnknown {
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
		case constant.PrometheusMetadataPath:
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
	result, err := metrics.Wrap(constant.ToolDiscoverMetrics, client.discover)(t.Context(), eventToolRequest(map[string]any{constant.ArgumentRegion: []any{"cn"}, constant.ArgumentTimeRange: "1h"}))
	if err != nil || result.IsError || requests.Load() != 2 {
		t.Fatalf("discovery=%+v, %v, catalog attempts=%d", result, err, requests.Load())
	}
	if got := counterValue(t, metrics.backendAttempts.WithLabelValues("prometheus", constant.PrometheusMetricNamesPath, "success")); got != 1 {
		t.Fatalf("successful name requests=%v", got)
	}
	if got := counterValue(t, metrics.backendAttempts.WithLabelValues("prometheus", constant.PrometheusMetricNamesPath, "retryable_error")); got != 1 {
		t.Fatalf("failed name attempts=%v", got)
	}
	if got := counterValue(t, metrics.retries.WithLabelValues("prometheus", constant.PrometheusMetricNamesPath)); got != 1 {
		t.Fatalf("name retries=%v", got)
	}
}

func TestQueryMetricsRequiresRangeAndOneTarget(t *testing.T) {
	client := &prometheusTools{maxRegions: 2}
	base := map[string]any{
		constant.ArgumentQuery:  `huatuo_cpu{host="node-1"}`,
		constant.ArgumentRegion: []any{"cn"},
		constant.ArgumentHost:   "node-1",
	}
	result, err := client.query(t.Context(), eventToolRequest(base))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(mcpgo.TextContent).Text, "time_range is required") {
		t.Fatalf("query() without time range = %+v, %v", result, err)
	}
	base[constant.ArgumentTimeRange] = "1h"
	base[constant.ArgumentContainerHost] = "pod-1"
	result, err = client.query(t.Context(), eventToolRequest(base))
	if err != nil || !result.IsError || !strings.Contains(result.Content[0].(mcpgo.TextContent).Text, "exactly one") {
		t.Fatalf("query() with two targets = %+v, %v", result, err)
	}
	delete(base, constant.ArgumentContainerHost)
	base[constant.ArgumentHost] = strings.Repeat("x", constant.MaxTargetValueLength+1)
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
	result, err := metrics.Wrap(constant.ToolQueryMetrics, client.query)(t.Context(), eventToolRequest(map[string]any{
		constant.ArgumentQuery: `huatuo_cpu{host="node-1"}`, constant.ArgumentRegion: []any{"cn", "us"},
		constant.ArgumentHost: "node-1", constant.ArgumentTimeRange: "1h",
	}))
	if err != nil || result.IsError {
		t.Fatalf("query: %+v, %v", result, err)
	}
	text := textResult(t, result)
	if !strings.Contains(text, `"mcp_truncated": true`) || !strings.Contains(text, "series limit reached") {
		t.Fatalf("missing truncation warning: %s", text)
	}
	if got := counterValue(t, metrics.calls.WithLabelValues(constant.ToolQueryMetrics, constant.ToolOutcomePartialSuccess, constant.ToolErrorNone)); got != 1 {
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
				if request.URL.Path == constant.PrometheusSeriesPath {
					series := make([]map[string]string, 0, len(request.URL.Query()[constant.PrometheusMatchParameter]))
					for _, matcher := range request.URL.Query()[constant.PrometheusMatchParameter] {
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
				} else if request.URL.Path == constant.PrometheusMetadataPath {
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
			result, err := metrics.Wrap(constant.ToolDiscoverMetrics, client.discover)(t.Context(), eventToolRequest(map[string]any{constant.ArgumentRegion: []any{"cn", "us"}, constant.ArgumentTimeRange: "1h"}))
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
			outcome := constant.ToolOutcomeSuccess
			if test.truncated || test.warning {
				outcome = constant.ToolOutcomePartialSuccess
			}
			if got := counterValue(t, metrics.calls.WithLabelValues(constant.ToolDiscoverMetrics, outcome, constant.ToolErrorNone)); got != 1 {
				t.Fatalf("%s calls=%v", outcome, got)
			}
		})
	}
}

func TestDiscoverMetricsReportsUnknownTargetedMetricAsPartial(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case constant.PrometheusMetricNamesPath:
			_, _ = w.Write([]byte(`{"status":"success","data":["huatuo_a","huatuo_b"]}`))
		case constant.PrometheusSeriesPath:
			if strings.Contains(request.URL.Query().Get(constant.PrometheusMatchParameter), `huatuo_b`) {
				http.Error(w, "unavailable", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"status":"success","data":[{"__name__":"huatuo_a","region":"cn","host":"node"}]}`))
		case constant.PrometheusLabelsPath:
			_, _ = w.Write([]byte(`{"status":"success","data":["__name__","host","region"]}`))
		case constant.PrometheusMetadataPath:
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
	result, err := metrics.Wrap(constant.ToolDiscoverMetrics, client.discover)(t.Context(), eventToolRequest(map[string]any{constant.ArgumentRegion: []any{"cn"}, constant.ArgumentTimeRange: "1h", constant.ArgumentMetricNames: []any{"huatuo_a", "huatuo_b"}}))
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
	if len(response.Metrics) != 2 || response.Metrics[0].Availability != constant.MetricAvailabilityPresent || response.Metrics[1].Availability != constant.MetricAvailabilityUnknown || len(response.Errors) != 0 || len(response.Status) != 1 || response.Status[0].Returned != 2 || !strings.Contains(strings.Join(response.Status[0].Warnings, " "), "unknown") {
		t.Fatalf("unverified candidate reported missing/complete: %+v", response)
	}
	if got := counterValue(t, metrics.calls.WithLabelValues(constant.ToolDiscoverMetrics, constant.ToolOutcomePartialSuccess, constant.ToolErrorNone)); got != 1 {
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
			result, err := metrics.Wrap(constant.ToolQueryMetrics, client.query)(t.Context(), eventToolRequest(map[string]any{
				constant.ArgumentRegion: []any{"cn"}, constant.ArgumentQuery: `huatuo_cpu{host="node-1"}`,
				constant.ArgumentHost: "node-1", constant.ArgumentTimeRange: "1h",
			}))
			if err != nil || result.IsError {
				t.Fatalf("query: %+v, %v", result, err)
			}
			if strings.Contains(textResult(t, result), "some series omitted") != test.warning || strings.Contains(textResult(t, result), "mcp_truncated") {
				t.Fatalf("warning response=%s", textResult(t, result))
			}
			outcome := constant.ToolOutcomeSuccess
			if test.warning {
				outcome = constant.ToolOutcomePartialSuccess
			}
			if got := counterValue(t, metrics.calls.WithLabelValues(constant.ToolQueryMetrics, outcome, constant.ToolErrorNone)); got != 1 {
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
		constant.ArgumentQuery:     `huatuo_cpu{host="node-1"}`,
		constant.ArgumentRegion:    []any{"cn"},
		constant.ArgumentHost:      "node-1",
		constant.ArgumentTimeRange: "1h",
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
