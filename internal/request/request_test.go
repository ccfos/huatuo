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

package request

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	return u
}

func TestBuildRequestSetsTargetAndClonesHeaders(t *testing.T) {
	headers := http.Header{"X-Test": []string{"original"}}

	req, err := buildRequest("127.0.0.1:8080", http.MethodGet, "/tasks?limit=1", nil, headers)
	if err != nil {
		t.Fatalf("buildRequest() error = %v", err)
	}

	if req.URL.Scheme != "http" {
		t.Fatalf("scheme = %q, want http", req.URL.Scheme)
	}
	if req.URL.Host != "127.0.0.1:8080" {
		t.Fatalf("host = %q, want 127.0.0.1:8080", req.URL.Host)
	}
	if req.URL.RequestURI() != "/tasks?limit=1" {
		t.Fatalf("request uri = %q, want /tasks?limit=1", req.URL.RequestURI())
	}

	headers.Set("X-Test", "changed")
	if req.Header.Get("X-Test") != "original" {
		t.Fatalf("header = %q, want cloned original", req.Header.Get("X-Test"))
	}
}

func TestEncodeBodySetsJSONContentType(t *testing.T) {
	body, headers, err := encodeBody(map[string]string{"name": "cpu"}, nil)
	if err != nil {
		t.Fatalf("encodeBody() error = %v", err)
	}
	if headers.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", headers.Get("Content-Type"))
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if strings.TrimSpace(string(raw)) != `{"name":"cpu"}` {
		t.Fatalf("body = %q, want JSON object", string(raw))
	}
}

func TestCheckResponseErrJSONBody(t *testing.T) {
	resp := &ServerResponse{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"message":"invalid filter"}`)),
		ReqURL:     mustParseURL(t, "http://127.0.0.1/tasks"),
	}

	err := checkResponseErr(resp)
	if err == nil {
		t.Fatalf("checkResponseErr() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "invalid filter") {
		t.Fatalf("error = %q, want invalid filter", err.Error())
	}
}

func TestCheckResponseErrTextBody(t *testing.T) {
	resp := &ServerResponse{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("backend down\n")),
		ReqURL:     mustParseURL(t, "http://127.0.0.1/tasks"),
	}

	err := checkResponseErr(resp)
	if err == nil {
		t.Fatalf("checkResponseErr() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "backend down") {
		t.Fatalf("error = %q, want backend down", err.Error())
	}
}

func TestHTTPErrorMesg(t *testing.T) {
	got := HTTPErrorMesg(io.NopCloser(strings.NewReader(`{"message":"not found"}`)))

	if got != "not found" {
		t.Fatalf("HTTPErrorMesg() = %q, want not found", got)
	}
}

// TestDoRequestResponseBodyReadable verifies that the response body is still
// readable after doRequest returns on a successful (200 OK) response.
func TestDoRequestResponseBodyReadable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL, http.NoBody)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	resp, err := doRequest(req)
	if err != nil {
		t.Fatalf("doRequest() error = %v", err)
	}
	defer resp.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll(resp.Body) error = %v, want nil — body should be readable", err)
	}
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("body = %q, want %q", string(body), "ok")
	}
}
