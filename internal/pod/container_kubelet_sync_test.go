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
	"strings"
	"testing"
)

// errReader is an io.ReadCloser that returns an error on Read.
type errReader struct {
	err error
}

func (r *errReader) Read(p []byte) (int, error) {
	return 0, r.err
}

func (r *errReader) Close() error {
	return nil
}

// TestHttpDoRequestBodyReadError verifies that httpDoRequest returns the
// body read error instead of silently ignoring it.
func TestHttpDoRequestBodyReadError(t *testing.T) {
	expectedErr := errors.New("simulated read error")

	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &errReader{err: expectedErr},
				Header:     make(http.Header),
			}, nil
		}),
	}

	_, err := httpDoRequest(client, "http://127.0.0.1:10255/pods")
	if err == nil {
		t.Fatal("httpDoRequest() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "simulated read error") {
		t.Fatalf("httpDoRequest() error = %q, want it to contain the body read error", err.Error())
	}
	if !strings.Contains(err.Error(), "read body") {
		t.Fatalf("httpDoRequest() error = %q, want it to contain 'read body'", err.Error())
	}
}

// TestHttpDoRequestSuccess verifies that httpDoRequest returns the body
// content on a successful 200 OK response.
func TestHttpDoRequestSuccess(t *testing.T) {
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	body, err := httpDoRequest(client, "http://127.0.0.1:10255/pods")
	if err != nil {
		t.Fatalf("httpDoRequest() error = %v, want nil", err)
	}
	if string(body) != "ok" {
		t.Fatalf("httpDoRequest() body = %q, want %q", string(body), "ok")
	}
}

// roundTripperFunc is an http.RoundTripper that calls a function.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
