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

package storage

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	es "github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/ccfos/huatuo/internal/httpserver/auth"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/utils"
	storagees "github.com/ccfos/huatuo/internal/storage/elasticsearch"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestReaderPreservesShardFailureDiagnostics(t *testing.T) {
	for _, operation := range []string{"search", "count", "tracers"} {
		t.Run(operation, func(t *testing.T) {
			reader := newTestReader(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"_shards":{"total":1000,"successful":43,"failed":957,"failures":[{"index":"events-2026.10.12","shard":2,"reason":{"type":"query_shard_exception","reason":"failed to create query","caused_by":{"type":"illegal_argument_exception","reason":"failed to parse date field"}}}]},"hits":{"hits":[]},"count":0}`)
			}, nil)
			var err error
			switch operation {
			case "search":
				_, err = reader.SearchEvents(t.Context(), &event.Query{})
			case "count":
				_, err = reader.Count(t.Context(), &event.Query{})
			default:
				_, err = reader.TracerNames(t.Context(), &event.Query{})
			}
			if !errors.Is(err, event.ErrIncompleteData) {
				t.Fatalf("incomplete response error lost: %v", err)
			}
			for _, expected := range []string{"failed_shards=957", "total_shards=1000", "successful_shards=43", "events-2026.10.12", "shard=2", "query_shard_exception", "caused_by:", "illegal_argument_exception", "failed to parse date field"} {
				if !strings.Contains(err.Error(), expected) {
					t.Errorf("missing %q in %v", expected, err)
				}
			}
			var diagnostic interface{ DiagnosticSummary() string }
			if !errors.As(err, &diagnostic) || diagnostic.DiagnosticSummary() != err.Error() {
				t.Fatalf("backend diagnostic unavailable: %v", err)
			}
		})
	}
}

func TestReaderUsesExplicitQueryDateFormat(t *testing.T) {
	start := time.Date(2026, 10, 15, 8, 33, 1, 489068212, time.UTC)
	end := start.Add(3 * time.Hour)
	for _, field := range queryTimeFieldNames() {
		for _, operation := range []string{"search", "count", "tracers"} {
			t.Run(field+"/"+operation, func(t *testing.T) {
				reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Query struct {
							Bool struct {
								Filter []struct {
									Range map[string]map[string]string `json:"range"`
								} `json:"filter"`
							} `json:"bool"`
						} `json:"query"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					var parameters map[string]string
					for _, filter := range body.Query.Bool.Filter {
						if filter.Range[field] != nil {
							parameters = filter.Range[field]
						}
					}
					if parameters["format"] != "strict_date_optional_time_nanos" || parameters["gte"] != start.Format(time.RFC3339Nano) || parameters["lte"] != end.Format(time.RFC3339Nano) {
						t.Errorf("date predicate depends on legacy mapping or loses precision: %+v", parameters)
						_, _ = io.WriteString(w, `{"_shards":{"failed":1,"failures":[{"reason":{"type":"parse_exception","reason":"failed to parse date field"}}]}}`)
						return
					}
					_, _ = io.WriteString(w, `{"_shards":{"failed":0},"hits":{"hits":[]},"count":1,"aggregations":{"terms":{"buckets":[{"key":"cpusys","doc_count":1}]}}}`)
				}, nil)
				reader.fields.times = []string{field}
				query := &event.Query{Region: "us01", StartTime: start, EndTime: end}
				var err error
				switch operation {
				case "search":
					_, err = reader.SearchEvents(t.Context(), query)
				case "count":
					_, err = reader.Count(t.Context(), query)
				default:
					_, err = reader.TracerNames(t.Context(), query)
				}
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestShardFailureRetryability(t *testing.T) {
	for _, test := range []struct {
		name, body string
		retryable  bool
	}{
		{name: "timeout", body: `{"timed_out":true}`, retryable: true},
		{name: "missing details", body: `{"_shards":{"failed":1}}`, retryable: true},
		{name: "transient shard", body: `{"_shards":{"failed":1,"failures":[{"reason":{"type":"unavailable_shards_exception","reason":"shard unavailable"}}]}}`, retryable: true},
		{name: "date format", body: `{"_shards":{"failed":957,"failures":[{"reason":{"type":"parse_exception","reason":"failed to parse date field [2026-10-15T08:33:01.489068212Z] with format [yyyy-MM-dd HH:mm:ss||epoch_millis]"}}]}}`},
		{name: "nested date parser", body: `{"_shards":{"failed":1,"failures":[{"reason":{"type":"query_shard_exception","caused_by":{"type":"illegal_argument_exception","caused_by":{"type":"date_time_parse_exception","reason":"Failed to parse with all enclosed parsers"}}}}]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var status queryStatus
			if err := json.Unmarshal([]byte(test.body), &status); err != nil {
				t.Fatal(err)
			}
			err := status.check()
			var classified event.RetryableError
			if !errors.Is(err, event.ErrIncompleteData) || !errors.As(err, &classified) || classified.Retryable() != test.retryable {
				t.Fatalf("wrong shard failure retryability: %v, want retryable=%t", err, test.retryable)
			}
		})
	}
}

func TestShardFailureDiagnosticsAreBounded(t *testing.T) {
	var status queryStatus
	if err := json.Unmarshal([]byte(`{"_shards":{"failed":957}}`), &status); err != nil {
		t.Fatal(err)
	}
	err := status.check()
	if err == nil || strings.Contains(err.Error(), "total_shards=") || strings.Contains(err.Error(), "successful_shards=") {
		t.Fatalf("missing shard counters became known values: %v", err)
	}
	for range MaxShardFailureSummaries + 2 {
		status.Shards.Failures = append(status.Shards.Failures, shardFailure{Index: "events", Reason: shardReason{Type: "query_shard_exception", Reason: "invalid query"}})
	}
	if err := status.check(); !strings.Contains(err.Error(), "2 more shard failure details omitted") || strings.Count(err.Error(), "index=") != MaxShardFailureSummaries {
		t.Fatalf("shard detail count is unbounded: %v", err)
	}
	status.Shards.Failures[0].Reason.Reason = strings.Repeat("x", 10000) + "\ntrailing reason"
	status.Shards.Failures[0].Reason.CausedBy = &shardReason{Type: "illegal_argument_exception", Reason: "cause\nwith newline"}
	status.Shards.Failures[0].Reason.CausedBy.CausedBy = &shardReason{Type: "parse_exception", Reason: "invalid date", CausedBy: &shardReason{Type: "deepest", Reason: "must be omitted"}}
	err = status.check()
	if len(err.Error()) > event.MaxBackendErrorBodyBytes+len("... (truncated)") || strings.ContainsAny(err.Error(), "\r\n") || strings.Contains(err.Error(), strings.Repeat("x", 1000)) || strings.Contains(err.Error(), "must be omitted") || !strings.Contains(err.Error(), "further causes omitted") {
		t.Fatalf("shard diagnostic is not bounded: %v", err)
	}
}

func newTestReader(t *testing.T, handler http.HandlerFunc, before func(context.Context) error) *Reader {
	t.Helper()
	backend := httptest.NewServer(handler)
	t.Cleanup(backend.Close)
	reader, err := New(&Config{Addresses: []string{backend.URL}, Index: "events", BeforeRequest: before})
	if err != nil {
		t.Fatal(err)
	}
	reader.fields, reader.fieldsAt = testFieldSchema(), time.Now()
	t.Cleanup(func() {
		if err := reader.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	return reader
}

func testFieldSchema() *fieldSchema {
	schema := &fieldSchema{exact: make(map[string][]exactField), times: []string{types.DocumentFieldUploadedTimestamp}}
	for _, name := range []string{types.DocumentFieldRegion, types.DocumentFieldHostname, types.DocumentFieldContainerHostname, types.DocumentFieldContainerHostNamespace, types.DocumentFieldTracerName} {
		schema.exact[name] = []exactField{{Name: name + ".keyword"}}
	}
	return schema
}

func TestReaderDoesNotProbeOrRetryBackend(t *testing.T) {
	var attempts atomic.Int64
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/events/_count" {
			t.Errorf("unexpected endpoint %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("incompatible media types reached backend: %v", r.Header)
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}, nil)
	if attempts.Load() != 0 {
		t.Fatal("reader probed backend at startup")
	}
	_, err := reader.Count(t.Context(), &event.Query{Region: "cn"})
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr.HTTPStatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("count error = %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("SDK retried: %d requests", attempts.Load())
	}
}

func TestReaderValidatesConnectionConfiguration(t *testing.T) {
	for _, cfg := range []*Config{
		nil,
		{},
		{Index: "events"},
		{Index: "events", Addresses: []string{"file:///tmp/events"}},
		{Index: "events", Addresses: []string{"http://user:pass@backend"}},
		{Index: "events", Addresses: []string{"http://backend?token=secret"}},
		{Index: "events", Addresses: []string{"http://backend"}, Username: "user"},
	} {
		if reader, err := New(cfg); err == nil {
			_ = reader.Close(t.Context())
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}

func TestReaderClosePreservesFrameworkConnectionPool(t *testing.T) {
	var connections atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"count":1}`)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	shared, err := storagees.NewReadClient(&storagees.ReadClientConfig{Config: es.Config{Addresses: []string{server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shared.Close() }()
	countShared := func() {
		t.Helper()
		response, err := (esapi.CountRequest{Index: []string{"events"}, Body: strings.NewReader(`{}`)}).Do(t.Context(), shared)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("framework request status = %d", response.StatusCode)
		}
	}
	countShared()
	reader, err := New(&Config{Addresses: []string{server.URL}, Index: "events"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(t.Context()) }()
	reader.fields, reader.fieldsAt = testFieldSchema(), time.Now()
	if _, err := reader.Count(t.Context(), &event.Query{}); err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 2 {
		t.Fatalf("MCP and framework share a connection pool: connections=%d", connections.Load())
	}
	if err := reader.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	countShared()
	if connections.Load() != 2 {
		t.Fatalf("closing MCP disrupted the framework's idle connection: connections=%d", connections.Load())
	}
}

func TestReaderUsesConfiguredCredentialsAndEndpointPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "mcp-user" || password != "mcp-pass" || r.URL.Path != "/prefix/events/_count" {
			t.Errorf("incorrect credentials or endpoint: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"count":1}`)
	}))
	defer server.Close()
	reader, err := New(&Config{Addresses: []string{server.URL + "/prefix"}, Index: "events", Username: "mcp-user", Password: "mcp-pass"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(t.Context()) }()
	reader.fields, reader.fieldsAt = testFieldSchema(), time.Now()
	if count, err := reader.Count(t.Context(), &event.Query{}); err != nil || count != 1 {
		t.Fatalf("count = %d, %v", count, err)
	}
}

func TestReaderChargesEachReadToBackend(t *testing.T) {
	var attempts atomic.Int64
	reader := newTestReader(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		_, _ = io.WriteString(w, `{"count":3,"_shards":{"failed":0}}`)
	}, utils.NewRateLimiter(1).AllowBackendRequest)
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "review-user"})
	if count, err := reader.Count(ctx, &event.Query{}); err != nil || count != 3 {
		t.Fatalf("count = %d, %v", count, err)
	}
	if _, err := reader.Count(ctx, &event.Query{}); !errors.Is(err, utils.ErrRateLimitExceeded) {
		t.Fatalf("backend rejection = %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("rejected request reached network: %d", attempts.Load())
	}
}

func TestSearchPreservesSourceLocatorsAndBoundsOneRequest(t *testing.T) {
	var attempts atomic.Int64
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if r.URL.Path != "/events/_search" || r.URL.Query().Get("scroll") != "" {
			t.Errorf("query retained a cursor: %s", r.URL.String())
		}
		var body struct {
			Size int                            `json:"size"`
			From int                            `json:"from"`
			Sort []map[string]map[string]string `json:"sort"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Size != event.MaxDocuments+1 || body.From != 0 || len(body.Sort) != 1 || body.Sort[0][types.DocumentFieldUploadedTimestamp]["order"] != "desc" {
			t.Errorf("search bounds/sort = %+v", body)
		}
		_, _ = io.WriteString(w, `{"timed_out":false,"_shards":{"failed":0},"hits":{"hits":[{"_id":"source-1","_source":{"uploaded_timestamp":"2026-10-12T10:00:00Z","region":"cn","hostname":"node-1","tracer_name":"cpusys","tracer_type":"autotracing","tracer_data":{"flamedata":[{"level":0,"value":10,"self":10,"label":"worker"}]}}}]}}`)
	}, nil)
	result, err := reader.SearchEvents(t.Context(), &event.Query{Region: "cn", Limit: event.MaxDocuments + 100, ProfileOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	documents := result.Documents
	if len(documents) != 1 || documents[0].RecordID != "source-1" || documents[0].Hostname != "node-1" || documents[0].Region != "cn" {
		t.Fatalf("documents = %+v", documents)
	}
	encoded, err := json.Marshal(documents[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"RecordID"`) || !strings.Contains(string(encoded), `"hostname":"node-1"`) || documents[0].Source == nil || documents[0].Source.RecordID != "source-1" || len(documents[0].ProfileFrames) != 1 {
		t.Fatalf("source envelope changed: %s", encoded)
	}
	if attempts.Load() != 1 {
		t.Fatalf("search requests = %d", attempts.Load())
	}
}

func TestSearchEventsReportsLookaheadTruncationWithoutInventingCount(t *testing.T) {
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Size int `json:"size"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Size != 3 {
			t.Errorf("lookahead size=%d", request.Size)
		}
		_, _ = io.WriteString(w, `{"hits":{"hits":[{"_id":"1","_source":{"uploaded_time":"2026-10-12T10:00:00Z"}},{"_id":"2","_source":{"uploaded_time":"2026-10-12T09:00:00Z"}},{"_id":"3","_source":{"uploaded_time":"2026-10-12T08:00:00Z"}}]}}`)
	}, nil)
	result, err := reader.SearchEvents(t.Context(), &event.Query{Region: "cn", Limit: 2})
	if err != nil || len(result.Documents) != 2 || !result.Truncated || result.TotalCount != nil || result.CountExact {
		t.Fatalf("search completeness=%+v, %v", result, err)
	}
}

func TestReaderSupportsES7ES8AndOpenSearchResponses(t *testing.T) {
	for _, version := range []struct{ name, aggregation, product string }{
		{"ES7", "terms", ""}, {"ES8", "sterms#terms", "Elasticsearch"}, {"OpenSearch", "terms", ""},
	} {
		t.Run(version.name, func(t *testing.T) {
			reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("incompatible media types: %v", r.Header)
				}
				if version.product != "" {
					w.Header().Set("X-Elastic-Product", version.product)
				}
				_, _ = io.WriteString(w, `{"aggregations":{"`+version.aggregation+`":{"buckets":[{"key":"cpusys","doc_count":7},{"key":"irqtracing","doc_count":2}]}}}`)
			}, nil)
			counts, err := reader.TracerCounts(t.Context(), &event.Query{Region: "cn", Limit: 100})
			if err != nil || len(counts) != 2 || counts[0].Name != "cpusys" || counts[0].Count != 7 {
				t.Fatalf("counts = %+v, %v", counts, err)
			}
			names, err := reader.TracerNames(t.Context(), &event.Query{Region: "cn", ProfileOnly: true, Limit: 100})
			if err != nil || len(names) != 2 || names[1] != "irqtracing" {
				t.Fatalf("names = %v, %v", names, err)
			}
		})
	}
}

func TestReaderRejectsIncompleteOrMalformedResponses(t *testing.T) {
	for _, test := range []struct {
		name, body, operation string
		decode                bool
	}{
		{"timed out", `{"timed_out":true,"hits":{"hits":[]}}`, "search", false},
		{"failed shard", `{"_shards":{"failed":1},"count":1}`, "count", false},
		{"missing hits", `{}`, "search", true},
		{"missing source", `{"hits":{"hits":[{"_id":"missing"}]}}`, "search", true},
		{"missing count", `{}`, "count", true},
		{"invalid count", `{"count":-1}`, "count", true},
		{"missing aggregation", `{}`, "terms", true},
		{"missing bucket count", `{"aggregations":{"terms":{"buckets":[{"key":"cpusys"}]}}}`, "terms", true},
		{"malformed JSON", `{`, "count", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := newTestReader(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, test.body) }, nil)
			var err error
			switch test.operation {
			case "search":
				_, err = reader.SearchEvents(t.Context(), &event.Query{})
			case "count":
				_, err = reader.Count(t.Context(), &event.Query{})
			default:
				_, err = reader.TracerNames(t.Context(), &event.Query{Region: "cn", Limit: 100})
			}
			if err == nil || errors.Is(err, event.ErrDecodeFailed) != test.decode {
				t.Fatalf("error = %v, decode=%t", err, test.decode)
			}
		})
	}
}

func TestNotFoundResponsesRemainErrors(t *testing.T) {
	for _, body := range []string{`{"error":"index_not_found_exception"}`, "gateway route missing"} {
		reader := newTestReader(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, body, http.StatusNotFound)
		}, nil)
		_, searchErr := reader.SearchEvents(t.Context(), &event.Query{})
		_, countErr := reader.Count(t.Context(), &event.Query{})
		_, tracerErr := reader.TracerNames(t.Context(), &event.Query{Region: "cn", ProfileOnly: true, Limit: 100})
		for _, err := range []error{searchErr, countErr, tracerErr} {
			var response *ResponseError
			if !errors.As(err, &response) || response.StatusCode != http.StatusNotFound || !strings.Contains(response.Body, body) {
				t.Fatalf("404 was swallowed or lost its cause: %v", err)
			}
		}
	}
}

func TestReaderBoundsDecompressedResponseAndErrorBody(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		reader := newTestReader(t, func(w http.ResponseWriter, _ *http.Request) {
			var output io.Writer = w
			if compressed {
				w.Header().Set("Content-Encoding", "gzip")
				zipper := gzip.NewWriter(w)
				defer zipper.Close()
				output = zipper
			}
			_, _ = io.WriteString(output, `{"count":1,"extra":"`+strings.Repeat("x", MaxESResponseBytes)+`"}`)
		}, nil)
		if _, err := reader.Count(t.Context(), &event.Query{}); !errors.Is(err, event.ErrDecodeFailed) || !strings.Contains(err.Error(), "response exceeds") {
			t.Fatalf("oversized response accepted: %v", err)
		}
	}
	finished := make(chan struct{})
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, strings.Repeat("x", 5000))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := reader.Count(ctx, &event.Query{})
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || !strings.HasSuffix(responseErr.Body, "... (truncated)") {
		t.Fatalf("error body was drained: %v", err)
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("closing bounded error body did not terminate stream")
	}
}

func TestReaderDecodesValidCompressedResponse(t *testing.T) {
	reader := newTestReader(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zipper := gzip.NewWriter(w)
		_, _ = io.WriteString(zipper, `{"count":3}`)
		_ = zipper.Close()
	}, nil)
	if count, err := reader.Count(t.Context(), &event.Query{}); err != nil || count != 3 {
		t.Fatalf("compressed response = %d, %v", count, err)
	}
}

func TestReaderHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	reader := newTestReader(t, func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := reader.Count(ctx, &event.Query{}); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not stop after cancellation")
	}
}

func TestReadTransportRejectsWriteAndCursorEndpoints(t *testing.T) {
	transport := &readTransport{}
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodPost, "/events/_bulk"},
		{http.MethodDelete, "/events/_doc/id"},
		{http.MethodPost, "/events/_delete_by_query"},
		{http.MethodPost, "/_search/scroll"},
		{http.MethodPut, "/events"},
		{http.MethodGet, "/events/_search"},
	} {
		request := httptest.NewRequest(endpoint.method, "http://backend"+endpoint.path, http.NoBody)
		response, err := transport.RoundTrip(request)
		if response != nil {
			_ = response.Body.Close()
		}
		if !errors.Is(err, event.ErrInvalidQuery) {
			t.Fatalf("endpoint allowed: %+v, %v", endpoint, err)
		}
	}
}
