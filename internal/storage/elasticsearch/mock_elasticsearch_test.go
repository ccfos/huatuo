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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/elastic/go-elasticsearch/v7"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var origTransport http.RoundTripper // used to save old value

type mockRoundTripper struct {
	mock.Mock
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	args := m.Called(req)

	resp := args.Get(0)
	if resp == nil {
		return nil, args.Error(1)
	}

	switch v := resp.(type) {
	case *http.Response:
		return v, args.Error(1)

	case func(*http.Request) *http.Response:
		return v(req), args.Error(1)

	default:
		panic(fmt.Sprintf("unexpected RoundTrip return type: %T", v))
	}
}

func newMockHTTPResponse(status int, body string, headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}

	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// newMockClient create and replace defaultTransport，and return mockRoundTripper
func newMockClient() http.RoundTripper {
	origTransport = defaultTransport
	defaultTransport = &mockRoundTripper{}
	return defaultTransport
}

// closeMockClient restore defaultTransport
func closeMockClient() {
	defaultTransport = origTransport
}

// newMockClientForWrite creates a mocked StorageClient for testing Write.
func newMockClientForWrite(t *testing.T, statusCode int, responseBody string) *StorageClient {
	t.Helper()

	rt := new(mockRoundTripper)

	rt.On("RoundTrip", mock.Anything).
		Return(func(req *http.Request) *http.Response {
			return newMockHTTPResponse(
				statusCode,
				responseBody,
				map[string]string{
					"X-Elastic-Product": "Elasticsearch",
					"Content-Type":      "application/json",
				},
			)
		}, nil)

	cfg := elasticsearch.Config{
		Addresses: []string{"http://mock"},
		Transport: rt,
	}

	client, err := elasticsearch.NewClient(cfg)
	if err != nil {
		t.Fatalf("failed to create es client: %v", err)
	}

	t.Cleanup(func() {
		rt.AssertExpectations(t)
	})

	return &StorageClient{
		client: client,
		index:  defaultIndex,
	}
}

// newMockClientForWriteWithError creates a mocked StorageClient whose requests
// fail at the transport level (RoundTrip returns an error).
func newMockClientForWriteWithError(t *testing.T) *StorageClient {
	t.Helper()

	// Mock transport to simulate network/request errors.
	rt := new(mockRoundTripper)
	rt.On("RoundTrip", mock.Anything).
		Return(nil, errors.New("simulated network error"))

	cfg := elasticsearch.Config{
		Addresses: []string{"http://mock"},
		Transport: rt,
	}

	client, err := elasticsearch.NewClient(cfg)
	require.NoError(t, err)

	t.Cleanup(func() {
		rt.AssertExpectations(t)
	})

	return &StorageClient{
		client: client,
		index:  defaultIndex,
	}
}

// newMockClientForWriteWithoutProductCheck creates a mocked StorageClient
// that skips Elasticsearch product verification during client creation.
func newMockClientForWriteWithoutProductCheck(t *testing.T, statusCode int, responseBody string) *StorageClient {
	t.Helper()

	// Mock transport to return a non-error HTTP response.
	rt := new(mockRoundTripper)
	rt.On("RoundTrip", mock.Anything).
		Return(func(req *http.Request) *http.Response {
			return newMockHTTPResponse(
				statusCode,
				responseBody,
				map[string]string{
					"Content-Type": "application/json",
				},
			)
		}, nil)

	cfg := elasticsearch.Config{
		Addresses: []string{"http://mock"},
		Transport: rt,
		// Disable product header check triggered by the initial GET / request.
		UseResponseCheckOnly: true,
	}

	client, err := elasticsearch.NewClient(cfg)
	require.NoError(t, err)

	t.Cleanup(func() {
		rt.AssertExpectations(t)
	})

	return &StorageClient{
		client: client,
		index:  defaultIndex,
	}
}
