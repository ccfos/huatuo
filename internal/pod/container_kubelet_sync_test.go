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

package pod

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
)

// TestHTTPDoRequestPropagatesBodyReadError reproduces issue #258: when a kubelet
// response body fails to read, the error must be surfaced to the caller rather
// than being swallowed and replaced with an empty body.
func TestHTTPDoRequestPropagatesBodyReadError(t *testing.T) {
	wantReadErr := errors.New("simulated mid-stream read failure")
	client := &http.Client{
		Transport: bodyReadErrorTransport{readErr: wantReadErr},
	}
	requestURL := "http://test.kubelet.invalid/pods"

	_, err := httpDoRequest(client, requestURL)
	if err == nil {
		t.Fatal("httpDoRequest() error = nil, want non-nil")
	}
	if !errors.Is(err, wantReadErr) {
		t.Errorf("errors.Is(httpDoRequest(%q) error, wantReadErr) = false, want true; error = %v", requestURL, err)
	}
	wantError := fmt.Sprintf("http: %s, read body: %v", requestURL, wantReadErr)
	if err.Error() != wantError {
		t.Errorf("httpDoRequest(%q) error = %q, want %q", requestURL, err, wantError)
	}
}

// TestHTTPDoRequestReturnsBodyOnSuccess confirms the happy path still works
// after the fix, guarding against regressions that drop the body.
func TestHTTPDoRequestReturnsBodyOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	requestURL := srv.URL + "/pods"
	body, err := httpDoRequest(srv.Client(), requestURL)
	if err != nil {
		t.Fatalf("httpDoRequest() error = %v, want nil", err)
	}
	wantBody := `{"ok":true}`
	if string(body) != wantBody {
		t.Errorf("httpDoRequest(%q) body = %q, want %q", requestURL, body, wantBody)
	}
}

// TestHTTPDoRequestReportsNonOKStatusAndBody makes sure the existing
// non-200 error path is unaffected by the read-error fix.
func TestHTTPDoRequestReportsNonOKStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `boom`)
	}))
	t.Cleanup(srv.Close)

	requestURL := srv.URL + "/pods"
	_, err := httpDoRequest(srv.Client(), requestURL)
	if err == nil {
		t.Fatal("httpDoRequest() error = nil, want non-nil")
	}
	wantError := fmt.Sprintf("http: %s, status: %d, body: boom", requestURL, http.StatusInternalServerError)
	if err.Error() != wantError {
		t.Errorf("httpDoRequest(%q) error = %q, want %q", requestURL, err, wantError)
	}
}

// bodyReadErrorTransport returns a response whose body read fails after a
// partial payload.
type bodyReadErrorTransport struct {
	readErr error
}

func (t bodyReadErrorTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(io.MultiReader(
			strings.NewReader(`{"partial":`),
			iotest.ErrReader(t.readErr),
		)),
	}, nil
}
