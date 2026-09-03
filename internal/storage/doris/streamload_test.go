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

package doris

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortenBackoff keeps retry tests from sleeping for whole seconds.
func shortenBackoff(t *testing.T) {
	t.Helper()

	initial, max := retryInitialInterval, retryMaxInterval
	retryInitialInterval, retryMaxInterval = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { retryInitialInterval, retryMaxInterval = initial, max })
}

type recordedRequest struct {
	label       string
	groupCommit string
	authorized  bool
	body        string
}

// loaderFor points a streamLoader at a test server, which needs the bare
// host:port the loader formats into its URL.
func loaderFor(server *httptest.Server, maxRetries int, groupCommit string) *streamLoader {
	return newStreamLoader(
		strings.TrimPrefix(server.URL, "http://"),
		"huatuo", "profiling_metadata", "user", "secret",
		maxRetries, groupCommit,
	)
}

func testRows() []map[string]any {
	return []map[string]any{{"id": "row-1", "data": "{}"}}
}

// newRecordingServer replies with the supplied bodies in order, repeating the
// last one once they run out.
func newRecordingServer(t *testing.T, replies ...func(w http.ResponseWriter)) (*httptest.Server, *[]recordedRequest) {
	t.Helper()

	var (
		mu       sync.Mutex
		requests []recordedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		index := len(requests)
		_, hasAuth := func() (string, bool) {
			user, pass, ok := r.BasicAuth()
			return user + pass, ok
		}()
		requests = append(requests, recordedRequest{
			label:       r.Header.Get("label"),
			groupCommit: r.Header.Get("group_commit"),
			authorized:  hasAuth,
			body:        string(body),
		})
		mu.Unlock()

		if index >= len(replies) {
			index = len(replies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		replies[index](w)
	}))
	t.Cleanup(server.Close)

	return server, &requests
}

func replySuccess(w http.ResponseWriter) {
	_, _ = w.Write([]byte(`{"Status":"Success","Message":"OK","NumberTotalRows":1}`))
}

func replyServerError(w http.ResponseWriter) {
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"Status":"Fail","Message":"internal"}`))
}

func TestStreamLoaderSucceeds(t *testing.T) {
	server, requests := newRecordingServer(t, replySuccess)

	if err := loaderFor(server, 3, GroupCommitOff).load(t.Context(), testRows()); err != nil {
		t.Fatalf("load() returned error: %v", err)
	}
	if len(*requests) != 1 {
		t.Errorf("load() made %d requests, want 1", len(*requests))
	}
}

// Retrying is only safe because every attempt sends identical rows, which the
// unique key collapses if an earlier attempt landed after all.
func TestStreamLoaderRetriesTransientFailureWithIdenticalPayload(t *testing.T) {
	shortenBackoff(t)
	server, requests := newRecordingServer(t, replyServerError, replyServerError, replySuccess)

	if err := loaderFor(server, 3, GroupCommitOff).load(t.Context(), testRows()); err != nil {
		t.Fatalf("load() returned error: %v", err)
	}

	got := *requests
	if len(got) != 3 {
		t.Fatalf("load() made %d requests, want 3", len(got))
	}
	for i, request := range got {
		if request.body != got[0].body {
			t.Errorf("attempt %d body = %q, want %q", i, request.body, got[0].body)
		}
	}
}

func TestStreamLoaderStopsAfterMaxRetries(t *testing.T) {
	shortenBackoff(t)
	server, requests := newRecordingServer(t, replyServerError)

	err := loaderFor(server, 2, GroupCommitOff).load(t.Context(), testRows())
	if err == nil {
		t.Fatal("load() returned nil, want error")
	}
	// One initial attempt plus two retries.
	if len(*requests) != 3 {
		t.Errorf("load() made %d requests, want 3", len(*requests))
	}
}

func TestStreamLoaderDoesNotRetryPermanentFailures(t *testing.T) {
	tests := []struct {
		name  string
		reply func(w http.ResponseWriter)
	}{
		{
			name: "http 4xx",
			reply: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"Status":"Fail","Message":"bad request"}`))
			},
		},
		{
			name: "data quality error",
			reply: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"Status":"Fail","Message":"[DATA_QUALITY_ERROR] too many filtered rows"}`))
			},
		},
		{
			name: "invalid argument",
			reply: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"Status":"Fail","Message":"[INVALID_ARGUMENT] label and group_commit can't be set at the same time"}`))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shortenBackoff(t)
			server, requests := newRecordingServer(t, tt.reply)

			err := loaderFor(server, 3, GroupCommitOff).load(t.Context(), testRows())
			if err == nil {
				t.Fatal("load() returned nil, want error")
			}
			if !isPermanentError(err) {
				t.Errorf("load() error = %v, want permanent", err)
			}
			if len(*requests) != 1 {
				t.Errorf("load() made %d requests, want 1", len(*requests))
			}
		})
	}
}

// The transaction committed and only visibility is pending, so the rows are
// in; retrying would be wasted work.
func TestStreamLoaderTreatsPublishTimeoutAsSuccess(t *testing.T) {
	server, requests := newRecordingServer(t, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"Status":"Publish Timeout","Message":"OK"}`))
	})

	if err := loaderFor(server, 3, GroupCommitOff).load(t.Context(), testRows()); err != nil {
		t.Errorf("load() returned error: %v", err)
	}
	if len(*requests) != 1 {
		t.Errorf("load() made %d requests, want 1", len(*requests))
	}
}

// The group_commit header is only sent when a mode asks for it, and a label
// is never sent at all — Doris rejects a request carrying both.
func TestStreamLoaderSendsGroupCommitHeaderAndNeverALabel(t *testing.T) {
	tests := []struct {
		name            string
		mode            string
		wantGroupCommit string
	}{
		{name: "off", mode: GroupCommitOff},
		{name: "empty means off", mode: ""},
		{name: "async", mode: GroupCommitAsync, wantGroupCommit: GroupCommitAsync},
		{name: "sync", mode: GroupCommitSync, wantGroupCommit: GroupCommitSync},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, requests := newRecordingServer(t, replySuccess)

			if err := loaderFor(server, 0, tt.mode).load(t.Context(), testRows()); err != nil {
				t.Fatalf("load() returned error: %v", err)
			}

			got := (*requests)[0]
			if got.label != "" {
				t.Errorf("load() sent label %q, want none", got.label)
			}
			if got.groupCommit != tt.wantGroupCommit {
				t.Errorf("group_commit header = %q, want %q", got.groupCommit, tt.wantGroupCommit)
			}
		})
	}
}

// The FE answers with a 307 to a BE on another host, and Go drops
// Authorization across hosts unless the redirect hook puts it back.
func TestStreamLoaderKeepsAuthorizationAcrossRedirect(t *testing.T) {
	backend, requests := newRecordingServer(t, replySuccess)

	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, backend.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(frontend.Close)

	if err := loaderFor(frontend, 0, GroupCommitOff).load(t.Context(), testRows()); err != nil {
		t.Fatalf("load() returned error: %v", err)
	}
	if len(*requests) != 1 {
		t.Fatalf("backend saw %d requests, want 1", len(*requests))
	}
	if !(*requests)[0].authorized {
		t.Error("redirected request lost its Authorization header")
	}
}

func TestStreamLoaderStopsOnCanceledContext(t *testing.T) {
	shortenBackoff(t)
	server, _ := newRecordingServer(t, replyServerError)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := loaderFor(server, 3, GroupCommitOff).load(ctx, testRows()); err == nil {
		t.Error("load() returned nil, want error")
	}
}
