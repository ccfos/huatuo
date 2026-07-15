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
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errorReadCloser struct {
	err    error
	closed bool
}

func (r *errorReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r *errorReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestHTTPDoRequestReturnsBodyReadError(t *testing.T) {
	readErr := errors.New("response body read failed")
	body := &errorReadCloser{err: readErr}
	client := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       body,
			}, nil
		}),
	}
	const requestURL = "http://kubelet.test/pods"

	_, err := httpDoRequest(client, requestURL)
	if !errors.Is(err, readErr) {
		t.Fatalf("httpDoRequest() error=%v, want wrapped error %v", err, readErr)
	}
	if !strings.Contains(err.Error(), requestURL) {
		t.Errorf("httpDoRequest() error=%q, want URL context %q", err, requestURL)
	}
	if !body.closed {
		t.Errorf("response body was not closed")
	}
}
