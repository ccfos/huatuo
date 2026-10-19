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

package elasticsearch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	es "github.com/elastic/go-elasticsearch/v8"
)

type deadlineRoundTripper func(*http.Request) (*http.Response, error)

func (f deadlineRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCompatClientBoundsResponseBodyLifetime(t *testing.T) {
	previous := defaultTransport
	t.Cleanup(func() { defaultTransport = previous })
	contexts := make(chan context.Context, 1)
	defaultTransport = deadlineRoundTripper(func(r *http.Request) (*http.Response, error) {
		contexts <- r.Context()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Elastic-Product": []string{"Elasticsearch"}}, Body: io.NopCloser(strings.NewReader(`{"version":{"number":"8.17.1"}}`)), Request: r}, nil
	})
	client, err := newCompatClient(&es.Config{Addresses: []string{"http://localhost:9200"}, DisableRetry: true})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Info()
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	requestContext := <-contexts
	deadline, ok := requestContext.Deadline()
	if !ok {
		t.Fatal("request has no deadline while response body is outstanding")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Minute {
		t.Fatalf("unexpected remaining response budget: %v", remaining)
	}
	if err := requestContext.Err(); err != nil {
		t.Fatalf("body context canceled before consumption: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := requestContext.Err(); err == nil {
		t.Fatal("closing response did not release its context")
	}
}

func TestCompatTransportHonorsEarlierCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	tr := &productHeaderTransport{inner: deadlineRoundTripper(func(r *http.Request) (*http.Response, error) {
		got, ok := r.Context().Deadline()
		if !ok || !got.Equal(want) {
			t.Errorf("deadline=%v, want %v", got, want)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:9200", http.NoBody)
	response, err := tr.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}
