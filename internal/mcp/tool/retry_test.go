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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	prombackend "github.com/ccfos/huatuo/internal/mcp/backend/metrics"
	"github.com/ccfos/huatuo/internal/mcp/backend/storage"
)

func TestBackendErrorClassDistinguishesResponseFromTransport(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{err: fmt.Errorf("query: %w", event.ErrIncompleteData), want: "incomplete_data"},
		{err: event.ErrDecodeFailed, want: "decode"},
		{err: prombackend.ErrDecodeFailed, want: "decode"},
		{err: event.ErrInvalidQuery, want: "invalid_query"},
		{err: event.ErrUnsupportedOp, want: "unsupported"},
		{err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, want: "transport"},
		{err: io.ErrUnexpectedEOF, want: "transport"},
		{err: prombackend.ErrTransport, want: "transport"},
		{err: testHTTPStatusError(400), want: "http_400"},
		{err: errors.New("unclassified"), want: "unknown"},
	} {
		if got := backendErrorClass(test.err); got != test.want {
			t.Errorf("backendErrorClass(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestDateParsingFailureReturnsCauseWithoutRetries(t *testing.T) {
	var attempts atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_field_caps") {
			_, _ = io.WriteString(w, `{"fields":{"uploaded_time":{"date":{"searchable":true,"aggregatable":true}}}}`)
			return
		}
		attempts.Add(1)
		_, _ = io.WriteString(w, `{"_shards":{"total":1000,"successful":43,"failed":957,"failures":[{"index":"events","shard":0,"reason":{"type":"illegal_argument_exception","reason":"failed to parse date field"}}]},"count":0}`)
	}))
	defer backend.Close()
	reader, err := storage.New(&storage.Config{Addresses: []string{backend.URL}, Index: "events"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(t.Context()) }()
	store := withEventRetry(reader, newRetryPolicy(time.Second, 2), nil)
	_, err = store.Count(t.Context(), &event.Query{})
	if !errors.Is(err, event.ErrIncompleteData) {
		t.Fatalf("incomplete response lost through retry layer: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("date parsing failure retried %d times", attempts.Load())
	}
	for _, expected := range []string{"failed_shards=957", "successful_shards=43", "failed to parse date field"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("missing %q from returned error: %v", expected, err)
		}
	}
}

type testHTTPStatusError int

func (e testHTTPStatusError) Error() string       { return http.StatusText(int(e)) }
func (e testHTTPStatusError) HTTPStatusCode() int { return int(e) }

func TestRetryBackendUsesConfiguredAttemptsAndBackoff(t *testing.T) {
	attempts := 0
	var retries []int
	policy := retryPolicy{
		Timeout:      time.Second,
		MaxRetries:   3,
		InitialDelay: time.Millisecond,
		Backoff: func(retry int, _ time.Duration) time.Duration {
			retries = append(retries, retry)
			return 0
		},
	}
	metrics, err := NewMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := retryBackend(
		t.Context(), "test", "query", "region=cn", policy, metrics, "result_count",
		func(string) int { return 1 },
		func(context.Context) (string, bool, error) {
			attempts++
			if attempts < 3 {
				return "", true, errors.New("temporary")
			}
			return "ok", false, nil
		},
	)
	if err != nil || result != "ok" {
		t.Fatalf("retryBackend() = %q, %v", result, err)
	}
	if attempts != 3 || !reflect.DeepEqual(retries, []int{1, 2}) {
		t.Fatalf("attempts/backoffs = %d/%v, want 3/[1 2]", attempts, retries)
	}
	if got := counterValue(t, metrics.backendAttempts.WithLabelValues("test", "query", "retryable_error")); got != 2 {
		t.Fatalf("retryable backend requests = %v, want 2", got)
	}
	if got := counterValue(t, metrics.backendAttempts.WithLabelValues("test", "query", "success")); got != 1 {
		t.Fatalf("successful backend requests = %v, want 1", got)
	}
	if got := counterValue(t, metrics.retries.WithLabelValues("test", "query")); got != 2 {
		t.Fatalf("backend retries = %v, want 2", got)
	}
}

func TestEventRetryUsesExplicitAdapterClassification(t *testing.T) {
	native := errors.New("native backend exception: code 241")
	for _, tt := range []struct {
		name     string
		err      error
		attempts int
	}{
		{name: "transient", err: &event.ReadError{Err: native, Transient: true}, attempts: 3},
		{name: "permanent", err: &event.ReadError{Err: native}, attempts: 1},
		{name: "unknown", err: native, attempts: 1},
		{name: "decode", err: &event.ReadError{Err: event.ErrDecodeFailed, Transient: true}, attempts: 1},
		{name: "canceled", err: &event.ReadError{Err: context.Canceled, Transient: true}, attempts: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			_, err := retryEventReader(t.Context(), "native", "count", "", retryPolicy{
				MaxRetries: 2, Backoff: func(int, time.Duration) time.Duration { return 0 },
			}, nil, func(int) int { return 0 }, func(context.Context) (int, error) {
				attempts++
				return 0, tt.err
			})
			if attempts != tt.attempts || !errors.Is(err, tt.err) {
				t.Fatalf("attempts=%d error=%v, want %d and preserved cause", attempts, err, tt.attempts)
			}
		})
	}
}

func TestRetryBackendHonorsTotalTimeout(t *testing.T) {
	attempts := 0
	temporaryErr := errors.New("temporary")
	policy := retryPolicy{
		Timeout:      10 * time.Millisecond,
		MaxRetries:   10,
		InitialDelay: time.Hour,
		Backoff:      func(_ int, delay time.Duration) time.Duration { return delay },
	}
	started := time.Now()
	_, err := retryBackend(
		t.Context(), "test", "query", "region=cn", policy, nil, "result_count",
		func(string) int { return 1 },
		func(context.Context) (string, bool, error) {
			attempts++
			return "", true, temporaryErr
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retryBackend() error = %v, want DeadlineExceeded", err)
	}
	if !errors.Is(err, temporaryErr) {
		t.Fatalf("retryBackend() error = %v, want underlying backend error", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("elapsed = %v, want near 10ms", elapsed)
	}
}

func TestRetryableElasticsearchStatuses(t *testing.T) {
	for _, status := range []int{429, 502, 503, 504} {
		if !retryableEventReaderError(&storage.ResponseError{StatusCode: status}) {
			t.Errorf("status %d was not retryable", status)
		}
	}
	if retryableEventReaderError(&storage.ResponseError{StatusCode: http.StatusBadRequest}) {
		t.Fatal("status 400 was retryable")
	}
}
