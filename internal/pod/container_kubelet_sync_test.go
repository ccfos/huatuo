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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReader) Close() error             { return nil }

func TestHTTPDoRequestReadBodyError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       errReader{},
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	_, err := httpDoRequest(client, "http://127.0.0.1/pods")
	if err == nil {
		t.Fatal("httpDoRequest() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "read body") {
		t.Fatalf("httpDoRequest() error = %q, want read body context", err)
	}
}

func TestHTTPDoRequestSuccess(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	body, err := httpDoRequest(client, "http://127.0.0.1/pods")
	if err != nil {
		t.Fatalf("httpDoRequest() error = %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("httpDoRequest() body = %q, want %q", string(body), "ok")
	}
}
