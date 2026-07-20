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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// failingReader returns its payload on the first Read and an error on the
// second, simulating a kubelet response whose body is truncated mid-stream.
type failingReader struct {
	first   []byte
	read    int
	failErr error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.read < len(f.first) {
		n := copy(p, f.first[f.read:])
		f.read += n
		return n, nil
	}
	return 0, f.failErr
}

func (f *failingReader) Close() error { return nil }

// TestHTTPDoRequestPropagatesBodyReadError reproduces issue #258: when a kubelet
// response body fails to read, the error must be surfaced to the caller rather
// than being swallowed and replaced with an empty body.
func TestHTTPDoRequestPropagatesBodyReadError(t *testing.T) {
	wantReadErr := errors.New("simulated mid-stream read failure")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200 OK, but the body errors partway through. This is what real
		// kubelets do when the connection drops after headers are flushed.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		//nolint:errcheck // the test intentionally ignores the Flush result
		_, _ = w.Write([]byte(`{"partial":`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hijack is not needed; instead, we swap the body by wrapping via a
		// transport below to keep this handler transport-agnostic.
	}))
	t.Cleanup(srv.Close)

	// Use a custom transport whose response body errors on read.
	client := &http.Client{
		Transport: bodyErrorTransport{failErr: wantReadErr},
	}

	_, err := httpDoRequest(client, "http://test.kubelet.invalid/pods")
	if err == nil {
		t.Fatalf("httpDoRequest returned nil error for a body read failure; expected the read error to propagate")
	}
	if !errors.Is(err, wantReadErr) {
		t.Errorf("expected error to wrap the read failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "http://test.kubelet.invalid/pods") {
		t.Errorf("expected error to retain URL context, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "read body") {
		t.Errorf("expected error message to mention 'read body', got %q", err.Error())
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

	body, err := httpDoRequest(srv.Client(), srv.URL+"/pods")
	if err != nil {
		t.Fatalf("unexpected error on success: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("expected body {\"ok\":true}, got %q", string(body))
	}
}

// TestHTTPDoRequestNonOKStatusKeepsErrorAndBody makes sure the existing
// non-200 error path is unaffected by the read-error fix.
func TestHTTPDoRequestNonOKStatusKeepsErrorAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `boom`)
	}))
	t.Cleanup(srv.Close)

	_, err := httpDoRequest(srv.Client(), srv.URL+"/pods")
	if err == nil {
		t.Fatalf("expected error for non-200 status, got nil")
	}
	if !strings.Contains(err.Error(), "status: 500") {
		t.Errorf("expected status in error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected body in error, got %q", err.Error())
	}
}

// bodyErrorTransport returns a 200 response whose body fails on the first
// Read, replicating the kubelet truncation scenario from the issue.
type bodyErrorTransport struct {
	failErr error
}

func (t bodyErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: &failingReader{
			first:   []byte(`{"partial":`),
			failErr: t.failErr,
		},
	}, nil
}
