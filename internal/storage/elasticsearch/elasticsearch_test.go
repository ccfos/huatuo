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
	"fmt"
	"net/http"
	"testing"

	"huatuo-bamai/internal/storage/types"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNewStorageClient_Success(t *testing.T) {
	rt := newMockClient().(*mockRoundTripper)
	defer closeMockClient()

	rt.On("RoundTrip", mock.Anything).
		Return(
			//nolint:bodyclose // mock response body, no real network resource to close
			newMockHTTPResponse(
				http.StatusOK,
				`{
					"name":"mock-node",
					"cluster_name":"mock-cluster",
					"cluster_uuid":"abc123",
					"version":{"number":"7.10.2"},
					"tagline":"You Know, for Search"
				}`,
				map[string]string{
					"Content-Type":      "application/json",
					"X-Elastic-Product": "Elasticsearch",
				},
			),
			nil,
		)

	client, err := NewStorageClient("http://mock-es:9200", "", "", "")
	require.NoError(t, err)
	require.Equal(t, defaultIndex, client.index)

	rt.AssertExpectations(t)
}

// TestNewStorageClient_InvalidURL tests behavior with malformed or unsupported URL schemes.
func TestNewStorageClient_InvalidURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"malformed URL", "http://[invalid]", true},
		{"unsupported scheme", "ftp://localhost:9200", true},
		{"empty URL", "", true},
		{"no scheme", "localhost:9200", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewStorageClient(tt.url, "", "", "")
			if tt.wantErr && err == nil {
				t.Fatal("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected nil but got err: %v", err)
			}
		})
	}
}

// TestNewStorageClient_MissingElasticHeader tests failure when server does not return X-Elastic-Product header.
// This is a security check in the official client.
func TestNewStorageClient_MissingElasticHeader(t *testing.T) {
	rt := newMockClient().(*mockRoundTripper)
	defer closeMockClient()

	rt.On("RoundTrip", mock.Anything).
		Return(
			//nolint:bodyclose // mock response body, no real network resource to close
			newMockHTTPResponse(
				http.StatusOK,
				`{"name":"mock","version":{"number":"7.10.2"}}`,
				nil, // no X-Elastic-Product
			),
			nil,
		)

	_, err := NewStorageClient("http://mock-es:9200", "", "", "")
	require.Error(t, err)

	rt.AssertExpectations(t)
}

// TestNewStorageClient_FailOnInfo tests failure when ES returns error status code.
func TestNewStorageClient_FailOnInfo(t *testing.T) {
	rt := newMockClient().(*mockRoundTripper)
	defer closeMockClient()

	rt.On("RoundTrip", mock.Anything).
		Return(
			//nolint:bodyclose // mock response body, no real network resource to close
			newMockHTTPResponse(
				http.StatusInternalServerError,
				"",
				map[string]string{
					"X-Elastic-Product": "Elasticsearch", // has X-Elastic-Product
				},
			),
			nil,
		)

	_, err := NewStorageClient("http://mock-es:9200", "", "", "")
	require.Error(t, err)

	rt.AssertExpectations(t)
}

// TestWrite_Success tests successful write operation.
func TestWrite_Success(t *testing.T) {
	client := newMockClientForWrite(t, 201, `{"result":"created"}`)

	doc := &types.Document{
		TracerID:   "test-id",
		TracerData: "test-data",
	}

	err := client.Write(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWrite_FailStatus tests write failure due to ES status code.
func TestWrite_FailStatus(t *testing.T) {
	client := newMockClientForWrite(t, 500, `{"error":"fail"}`)

	doc := &types.Document{
		TracerID:   "test-id",
		TracerData: "test-data",
	}

	err := client.Write(doc)
	if err == nil {
		t.Fatal("expected error due to ES status code")
	}
}

// TestWrite_EmptyDocument tests Write with minimal/empty document fields.
func TestWrite_EmptyDocument(t *testing.T) {
	tests := []struct {
		name string
		doc  *types.Document
	}{
		{"nil document", nil},
		{"empty document", &types.Document{}},
		{"only tracer ID", &types.Document{TracerID: "empty-id"}},
		{"only tracer data", &types.Document{TracerData: "empty-data"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newMockClientForWrite(t, 201, `{"result":"created","_id":"test-id"}`)

			err := client.Write(tt.doc)
			require.NoError(t, err)
		})
	}
}

// TestWrite_Non200Success tests handling of non-200 but accepted status codes (e.g., 201 Created).
func TestWrite_Non200Success(t *testing.T) {
	client := newMockClientForWrite(t, 201, `{"result":"created","_id":"abc123"}`)

	err := client.Write(&types.Document{TracerID: "id"})
	if err != nil {
		t.Fatalf("unexpected error on status 201: %v", err)
	}
}

// The following tests verify Write error handling across different failure stages:
// JSON marshaling, request execution, response status validation, and response parsing.
func TestWrite_JSONMarshalError(t *testing.T) {
	client := &StorageClient{
		// The es client can be nil because JSON marshal will fail first
		// and the client won't be used
		client: nil,
		index:  "test-index",
	}

	doc := &types.Document{
		TracerData: badMarshalType{},
	}

	err := client.Write(doc)

	require.Error(t, err)
	require.Contains(t, err.Error(), "json Marshal")
}

func TestWrite_IndexRequestDoError(t *testing.T) {
	client := newMockClientForWriteWithError(t)

	doc := &types.Document{
		TracerID: "test",
	}

	err := client.Write(doc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "error getting response")
}

func TestWrite_ReturnsErrorStatus(t *testing.T) {
	client := newMockClientForWriteWithoutProductCheck(t, 300, `{"result":"created","_id":"abc123"}`)

	err := client.Write(&types.Document{TracerID: "test"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "index document failed with status: 300")
}

func TestWrite_InvalidJSONResponse(t *testing.T) {
	client := newMockClientForWrite(t, 200, `this is not json`)

	err := client.Write(&types.Document{TracerID: "test"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse response body")
}

type badMarshalType struct{}

func (b badMarshalType) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("forced marshal error")
}
