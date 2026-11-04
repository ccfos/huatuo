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
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpevent "github.com/ccfos/huatuo/internal/mcp/backend/event"
)

func newTestReader(t *testing.T, input *Config, handler http.HandlerFunc) *Reader {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := *input
	cfg.Address = server.URL + cfg.Address
	reader, err := New(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	return reader
}

func TestReaderPreservesAuthenticationBasePathAndForm(t *testing.T) {
	for _, test := range []struct {
		name   string
		cfg    Config
		header string
	}{
		{name: "anonymous"},
		{name: "bearer", cfg: Config{AuthType: "bearer", AuthToken: "secret"}, header: "Bearer secret"},
		{name: "basic", cfg: Config{AuthType: "basic", AuthUser: "user", AuthPass: "pass"}, header: "Basic dXNlcjpwYXNz"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.cfg.Address = "/prometheus"
			reader := newTestReader(t, &test.cfg, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/prometheus/api/v1/query_range" || r.Method != http.MethodPost || r.Header.Get("Authorization") != test.header {
					t.Errorf("incorrect request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("query") != `huatuo_cpu{host="node-1",region="cn"}` || r.Form.Get("step") != "1m" {
					t.Errorf("incorrect form: %v", r.Form)
				}
				_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
			})
			_, err := reader.Request(t.Context(), http.MethodPost, "/api/v1/query_range", url.Values{"query": {`huatuo_cpu{host="node-1",region="cn"}`}, "step": {"1m"}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReaderRejectsInvalidConfiguration(t *testing.T) {
	for _, cfg := range []*Config{
		nil,
		{},
		{Address: "file:///prometheus"},
		{Address: "http://prometheus", AuthType: "digest"},
		{Address: "http://prometheus", AuthType: "bearer"},
		{Address: "http://prometheus", AuthType: "basic"},
		{Address: "http://prometheus", AuthToken: "secret"},
		{Address: "http://prometheus", AuthType: "basic", AuthUser: "user", AuthToken: "secret"},
	} {
		if reader, err := New(cfg); err == nil {
			_ = reader.Close(t.Context())
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}

func TestReaderNeitherProbesNorRetries(t *testing.T) {
	var attempts atomic.Int64
	reader := newTestReader(t, &Config{}, func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	})
	if attempts.Load() != 0 {
		t.Fatal("reader probed during startup")
	}
	_, err := reader.Request(t.Context(), http.MethodGet, "/api/v1/query", nil)
	var status *ResponseError
	if !errors.As(err, &status) || status.HTTPStatusCode() != http.StatusServiceUnavailable || attempts.Load() != 1 {
		t.Fatalf("attempts=%d error=%v", attempts.Load(), err)
	}
}

func TestReaderPreservesPrometheusAPIErrorReason(t *testing.T) {
	reader := newTestReader(t, &Config{}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"status":"error","errorType":"bad_data","error":"invalid parameter query: parse error"}`)
	})
	_, err := reader.Request(t.Context(), http.MethodGet, PrometheusRangeQueryPath, nil)
	var response *ResponseError
	if !errors.As(err, &response) || response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), "bad_data") || !strings.Contains(err.Error(), "invalid parameter query: parse error") {
		t.Fatalf("backend API error detail lost: %v", err)
	}
}

func TestReaderPreservesNonJSONErrorBodies(t *testing.T) {
	for _, body := range []string{"upstream authentication failed", "<html>proxy route not found</html>", "upstream unavailable " + strings.Repeat("x", 10000)} {
		reader := newTestReader(t, &Config{}, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, body)
		})
		_, err := reader.Request(t.Context(), http.MethodGet, PrometheusRangeQueryPath, nil)
		var response *ResponseError
		if !errors.As(err, &response) || response.StatusCode != http.StatusBadGateway || !strings.Contains(response.Detail, body[:min(len(body), 20)]) || len(response.Detail) > mcpevent.MaxBackendErrorBodyBytes+3 {
			t.Fatalf("raw proxy/backend error lost or unbounded: %v", err)
		}
		if len(body) > mcpevent.MaxBackendErrorBodyBytes && !strings.HasSuffix(response.Detail, "...") {
			t.Fatalf("truncation not marked: %v", err)
		}
	}
}

func TestReaderRejectsForbiddenEndpointsBeforeChargingOrDispatch(t *testing.T) {
	var attempts, charges atomic.Int64
	reader := newTestReader(t, &Config{BeforeRequest: func(context.Context) error { charges.Add(1); return nil }}, func(w http.ResponseWriter, _ *http.Request) { attempts.Add(1); w.WriteHeader(http.StatusOK) })
	for _, test := range []struct{ method, path string }{
		{http.MethodDelete, "/api/v1/query"},
		{http.MethodPost, "/api/v1/admin/tsdb/delete_series"},
		{http.MethodPost, "/api/v1/admin/tsdb/snapshot"},
		{http.MethodPost, "/-/reload"},
		{http.MethodPost, "/api/v1/series"},
		{http.MethodGet, "/api/v1/status/config"},
	} {
		if _, err := reader.Request(t.Context(), test.method, test.path, nil); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("forbidden endpoint accepted: %+v, %v", test, err)
		}
	}
	if attempts.Load() != 0 || charges.Load() != 0 {
		t.Fatalf("forbidden requests dispatched=%d charged=%d", attempts.Load(), charges.Load())
	}
}

func TestReaderDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Int64
	reader := newTestReader(t, &Config{AuthType: "bearer", AuthToken: "secret"}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/query" {
			w.Header().Set("Location", "/api/v1/admin/tsdb/snapshot")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		redirected.Add(1)
	})
	_, err := reader.Request(t.Context(), http.MethodPost, "/api/v1/query", nil)
	var status *ResponseError
	if !errors.As(err, &status) || status.StatusCode != http.StatusTemporaryRedirect || redirected.Load() != 0 {
		t.Fatalf("redirect error=%v dispatched=%d", err, redirected.Load())
	}
}

func TestReaderPreservesBeforeRequestRejection(t *testing.T) {
	rejected := errors.New("caller budget exhausted")
	reader := newTestReader(t, &Config{BeforeRequest: func(context.Context) error { return rejected }}, func(http.ResponseWriter, *http.Request) { t.Error("rejected request dispatched") })
	if _, err := reader.Request(t.Context(), http.MethodGet, PrometheusMetricNamesPath, nil); !errors.Is(err, rejected) {
		t.Fatalf("rejection error=%v", err)
	}
}

func TestReaderBoundsDecompressedResponses(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		reader := newTestReader(t, &Config{}, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			zipper := gzip.NewWriter(w)
			defer zipper.Close()
			if oversized {
				_, _ = io.WriteString(zipper, `{"status":"success","extra":"`+strings.Repeat("x", MaxPrometheusResponse)+`"}`)
			} else {
				_, _ = io.WriteString(zipper, `{"status":"success","data":[]}`)
			}
		})
		data, err := reader.Request(t.Context(), http.MethodGet, PrometheusMetricNamesPath, nil)
		if oversized && !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("oversized gzip response=%v", err)
		}
		if !oversized && (err != nil || !strings.Contains(string(data), `"status":"success"`)) {
			t.Fatalf("gzip response=%s, %v", data, err)
		}
	}
}

func TestReaderRejectsSemanticAPIError(t *testing.T) {
	reader := newTestReader(t, &Config{}, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"error","errorType":"execution","error":"query timed out"}`)
	})
	_, err := reader.Request(t.Context(), http.MethodGet, "/api/v1/query", nil)
	if !errors.Is(err, ErrDecodeFailed) || !strings.Contains(err.Error(), "Prometheus query failed (execution): query timed out") {
		t.Fatalf("semantic error=%v", err)
	}
}

func TestReaderHonorsPerAttemptTimeout(t *testing.T) {
	reader := newTestReader(t, &Config{Timeout: 30 * time.Millisecond}, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	_, err := reader.Request(t.Context(), http.MethodGet, "/api/v1/query", nil)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrTransport) {
		t.Fatalf("timeout=%v", err)
	}
}
