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

package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	clientmodel "github.com/prometheus/client_model/go"
)

func TestHTTPMetricsBoundUnmatchedMethodLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := NewServer(&Config{PromReg: registry})
	server.MustRegisterRoutes("", []Route{{
		Method: "PROPFIND",
		Path:   "/documents",
		Handler: func(ctx *Context) error {
			ctx.Status(http.StatusNoContent)
			return nil
		},
	}})

	for i := range 128 {
		request := httptest.NewRequest(fmt.Sprintf("X-PROBE-%d", i), "/unknown", nil)
		server.engine.ServeHTTP(httptest.NewRecorder(), request)
	}
	server.engine.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/unknown", nil),
	)
	server.engine.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest("PROPFIND", "/documents", nil),
	)

	wantMethods := map[string]map[string]float64{
		"huatuo_http_server_request_duration_seconds": {
			"GET":      1,
			"PROPFIND": 1,
			"other":    128,
		},
		"huatuo_http_server_requests_total": {
			"GET":      1,
			"PROPFIND": 1,
			"other":    128,
		},
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather HTTP metrics: %v", err)
	}
	for _, family := range families {
		want, ok := wantMethods[family.GetName()]
		if !ok {
			continue
		}
		got := methodObservations(family)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s method observations mismatch (-want +got):\n%s", family.GetName(), diff)
		}
		delete(wantMethods, family.GetName())
	}
	if len(wantMethods) != 0 {
		t.Fatalf("missing HTTP metric families: %v", wantMethods)
	}
}

func methodObservations(family *clientmodel.MetricFamily) map[string]float64 {
	result := make(map[string]float64, len(family.Metric))
	for _, metric := range family.Metric {
		var method string
		for _, label := range metric.Label {
			if label.GetName() == "method" {
				method = label.GetValue()
				break
			}
		}
		switch family.GetType() {
		case clientmodel.MetricType_COUNTER:
			result[method] += metric.GetCounter().GetValue()
		case clientmodel.MetricType_HISTOGRAM:
			result[method] += float64(metric.GetHistogram().GetSampleCount())
		}
	}
	return result
}
