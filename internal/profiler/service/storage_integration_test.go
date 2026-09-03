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

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

const elasticsearchIntegrationAddressEnv = "HUATUO_ES_TEST_ADDR"

func TestProfileStorageSearchProfilesElasticsearch(t *testing.T) {
	address := strings.TrimRight(os.Getenv(elasticsearchIntegrationAddressEnv), "/")
	if address == "" {
		t.Skipf("set %s to run the Elasticsearch integration test", elasticsearchIntegrationAddressEnv)
	}

	index := fmt.Sprintf("huatuo-profile-storage-test-%d", time.Now().UnixNano())
	storage, err := NewProfileStorageContext(t.Context(), address, "", "", index)
	if err != nil {
		t.Fatalf("NewProfileStorageContext() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(context.Background()); closeErr != nil {
			t.Errorf("close profile storage: %v", closeErr)
		}
	})
	t.Cleanup(func() {
		requestElasticsearch(t, context.Background(), http.MethodDelete, address+"/"+index, nil)
	})

	documents := []struct {
		storageID  string
		tracerID   string
		capturedAt string
	}{
		{storageID: "window-a", tracerID: "trace-old", capturedAt: "2026-08-16T01:00:00.000000001Z"},
		{storageID: "window-b", tracerID: "trace-shared", capturedAt: "2026-08-16T02:00:00.000000001Z"},
		{storageID: "window-c", tracerID: "trace-shared", capturedAt: "2026-08-16T02:00:00.000000001Z"},
		{storageID: "window-d", tracerID: "trace-new", capturedAt: "2026-08-16T03:00:00.000000001Z"},
	}
	for _, document := range documents {
		body := fmt.Appendf(nil, `{
			"hostname":"node-1",
			"region":"integration",
			"profile_storage_id":%q,
			"uploaded_time":%q,
			"tracer_id":%q,
			"tracer_time":%q
		}`, document.storageID, document.capturedAt, document.tracerID, document.capturedAt)
		requestElasticsearch(
			t,
			t.Context(),
			http.MethodPut,
			fmt.Sprintf("%s/%s/_doc/%s?refresh=true", address, index, document.storageID),
			body,
		)
	}
	for _, field := range []string{"id", "tracer"} {
		values, err := storage.AggregationsByFieldContext(
			t.Context(),
			&SearchFilter{Hostname: "node-1", Limit: 10},
			field,
		)
		if err != nil {
			t.Fatalf("AggregationsByFieldContext(%q) error = %v", field, err)
		}
		slices.Sort(values)
		want := []string{"trace-new", "trace-old", "trace-shared"}
		if !slices.Equal(values, want) {
			t.Errorf("AggregationsByFieldContext(%q) = %#v, want %#v", field, values, want)
		}
	}

	filter := &SearchFilter{Hostname: "node-1", Limit: 2}
	firstPage, cursor, err := storage.SearchProfilesPageContext(t.Context(), filter, nil)
	if err != nil {
		t.Fatalf("first SearchProfilesPageContext() error = %v", err)
	}
	assertProfileOrder(t, firstPage, "trace-new/window-d", "trace-shared/window-b")
	if len(cursor) != 3 {
		t.Fatalf("first cursor = %#v, want three sort values", cursor)
	}

	secondPage, cursor, err := storage.SearchProfilesPageContext(t.Context(), filter, cursor)
	if err != nil {
		t.Fatalf("second SearchProfilesPageContext() error = %v", err)
	}
	assertProfileOrder(t, secondPage, "trace-shared/window-c", "trace-old/window-a")
	if len(cursor) != 3 {
		t.Fatalf("second cursor = %#v, want three sort values", cursor)
	}

	lastPage, cursor, err := storage.SearchProfilesPageContext(t.Context(), filter, cursor)
	if err != nil {
		t.Fatalf("last SearchProfilesPageContext() error = %v", err)
	}
	assertProfileOrder(t, lastPage)
	if cursor != nil {
		t.Fatalf("last cursor = %#v, want nil", cursor)
	}
}

func requestElasticsearch(t *testing.T, ctx context.Context, method, target string, body []byte) {
	t.Helper()

	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create Elasticsearch %s request: %v", method, err)
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Elasticsearch %s %s: %v", method, target, err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read Elasticsearch %s %s response: %v", method, target, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		t.Fatalf(
			"Elasticsearch %s %s returned status %d: %s",
			method,
			target,
			response.StatusCode,
			strings.TrimSpace(string(responseBody)),
		)
	}
}

func assertProfileOrder(t *testing.T, documents []*ProfileDocument, want ...string) {
	t.Helper()

	if len(documents) != len(want) {
		t.Fatalf("profile count = %d, want %d", len(documents), len(want))
	}
	for i, expected := range want {
		if documents[i] == nil {
			t.Fatalf("profile %d = nil, want %q", i, expected)
		}
		got := documents[i].TracerID + "/" + documents[i].ProfileStorageID
		if got != expected {
			t.Errorf("profile %d = %q, want %q", i, got, expected)
		}
	}
}
