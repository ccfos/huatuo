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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	eventstore "github.com/ccfos/huatuo/internal/mcp/backend/escompatible"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
)

func TestDedicatedReaderKeepsMCPRetryAndAttemptMetrics(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		var attempts atomic.Int64
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/_field_caps") {
				_, _ = w.Write([]byte(`{"fields":{"region":{"keyword":{"searchable":true,"aggregatable":true}},"tracer_type":{"keyword":{"searchable":true,"aggregatable":true}},"uploaded_time":{"date":{"searchable":true,"aggregatable":true}}}}`))
				return
			}
			if r.URL.Path != "/events/_count" {
				t.Errorf("unexpected endpoint %s", r.URL.Path)
			}
			if attempts.Add(1) == 1 {
				http.Error(w, "temporarily unavailable", status)
				return
			}
			_, _ = io.WriteString(w, `{"count":7}`)
		}))
		reader, err := eventstore.New(&eventstore.Config{Addresses: []string{backend.URL}, Index: "events"})
		if err != nil {
			backend.Close()
			t.Fatal(err)
		}
		metrics, err := NewMetrics(nil)
		if err != nil {
			backend.Close()
			t.Fatal(err)
		}
		policy := newRetryPolicy(time.Second, 2)
		policy.Backoff = func(int, time.Duration) time.Duration { return 0 }
		count, err := withEventRetry(reader, policy, metrics).Count(t.Context(), &event.Query{Region: "cn"})
		_ = reader.Close(context.Background())
		backend.Close()
		if err != nil || count != 7 || attempts.Load() != 2 {
			t.Fatalf("count=%d requests=%d error=%v", count, attempts.Load(), err)
		}
		firstOutcome := "retryable_error"
		if status == http.StatusTooManyRequests {
			firstOutcome = "rate_limited"
		}
		if got := counterValue(t, metrics.backendAttempts.WithLabelValues("elasticsearch", "count_events", firstOutcome)); got != 1 {
			t.Fatalf("first attempt count=%v", got)
		}
		if got := counterValue(t, metrics.backendAttempts.WithLabelValues("elasticsearch", "count_events", "success")); got != 1 {
			t.Fatalf("successful attempt count=%v", got)
		}
		if got := counterValue(t, metrics.retries.WithLabelValues("elasticsearch", "count_events")); got != 1 {
			t.Fatalf("retry count=%v", got)
		}
	}
}

func TestDedicatedReaderDecodeFailureIsNotRetried(t *testing.T) {
	var attempts atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		_, _ = io.WriteString(w, `{`)
	}))
	defer backend.Close()
	reader, err := eventstore.New(&eventstore.Config{Addresses: []string{backend.URL}, Index: "events"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(t.Context()) }()
	_, err = withEventRetry(reader, newRetryPolicy(time.Second, 2), nil).Count(t.Context(), &event.Query{Region: "cn"})
	if err == nil || attempts.Load() != 1 {
		t.Fatalf("malformed response retried: attempts=%d err=%v", attempts.Load(), err)
	}
}
