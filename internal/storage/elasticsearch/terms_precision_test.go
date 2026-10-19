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
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/elastic/go-elasticsearch/v8/esapi"

	"github.com/ccfos/huatuo/internal/storage/driver"
)

func TestStorageTermsIntegerPrecision(t *testing.T) {
	tests := []struct {
		name    string
		agg     string
		buckets string
		want    []string
	}{
		{
			name: "unsigned adjacent beyond exact float integers", agg: "ulterms#terms",
			buckets: `[{"key":9007199254740992,"doc_count":1},{"key":9007199254740993,"doc_count":1}]`,
			want:    []string{"9007199254740992", "9007199254740993"},
		},
		{
			name: "unsigned maximum adjacent", agg: "ulterms#terms",
			buckets: `[{"key":18446744073709551614,"doc_count":1},{"key":18446744073709551615,"doc_count":1}]`,
			want:    []string{"18446744073709551614", "18446744073709551615"},
		},
		{
			name: "untyped signed extremes", agg: "terms",
			buckets: `[{"key":-9223372036854775808,"doc_count":1},{"key":9223372036854775807,"doc_count":1}]`,
			want:    []string{"-9223372036854775808", "9223372036854775807"},
		},
		{
			name: "untyped unsigned maximum", agg: "terms",
			buckets: `[{"key":18446744073709551615,"doc_count":1}]`,
			want:    []string{"18446744073709551615"},
		},
		{
			name: "typed signed integers", agg: "lterms#terms",
			buckets: `[{"key":9007199254740993,"doc_count":1},{"key":9223372036854775807,"doc_count":1}]`,
			want:    []string{"9007199254740993", "9223372036854775807"},
		},
		{
			name: "literal strings", agg: "sterms#terms",
			buckets: `[{"key":"001","doc_count":1},{"key":"1.0","doc_count":1}]`,
			want:    []string{"001", "1.0"},
		},
		{
			name: "typed floating values", agg: "dterms#terms",
			buckets: `[{"key":1.50,"doc_count":1},{"key":1e3,"doc_count":1}]`,
			want:    []string{"1.5", "1000"},
		},
		{
			name: "unmapped empty terms", agg: "umterms#terms", buckets: `[]`, want: []string{},
		},
		{
			name: "small integer terms", agg: "terms",
			buckets: `[{"key":0,"doc_count":1},{"key":1,"doc_count":1},{"key":42,"doc_count":1}]`,
			want:    []string{"0", "1", "42"},
		},
		{
			name: "untyped floating values", agg: "terms",
			buckets: `[{"key":0.25,"doc_count":1},{"key":1e3,"doc_count":1},{"key":1.0,"doc_count":1}]`,
			want:    []string{"0.25", "1000", "1"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"timed_out":false,"_shards":{"total":1,"successful":1,"failed":0},"aggregations":{%q:{"buckets":%s}}}`, test.agg, test.buckets)
			backend := newResponseBackend(t, http.StatusOK, body)
			values, err := backend.Values(t.Context(), "counter", driver.Query{}, 10)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(values, test.want) {
				t.Fatalf("Values() = %q, want %q", values, test.want)
			}
		})
	}
}

func TestStorageTermsMalformedTypedValues(t *testing.T) {
	for _, kind := range []string{"lterms", "dterms"} {
		t.Run(kind, func(t *testing.T) {
			body := fmt.Sprintf(`{"timed_out":false,"_shards":{"total":1,"successful":1,"failed":0},"aggregations":{"%s#terms":{"buckets":[{"key":"not-a-number","doc_count":1}]}}}`, kind)
			backend := newResponseBackend(t, http.StatusOK, body)
			if _, err := backend.Values(t.Context(), "counter", driver.Query{}, 10); err == nil {
				t.Fatal("Values() accepted a malformed numeric term key")
			}
		})
	}
}

func TestStorageTermsLiveOpenSearch(t *testing.T) {
	address := os.Getenv("HUATUO_TERMS_TEST_OPENSEARCH_URL")
	if address == "" {
		t.Skip("set HUATUO_TERMS_TEST_OPENSEARCH_URL to run the OpenSearch integration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	index := "huatuo-terms-precision-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	backend, err := NewBackend(&Config{Addresses: []string{address}, Index: index})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if err := backend.Close(cleanupCtx); err != nil {
			t.Errorf("close backend: %v", err)
		}
		res, err := (esapi.IndicesDeleteRequest{Index: []string{index}}).Do(cleanupCtx, backend.transport)
		if err != nil {
			t.Errorf("delete integration index: %v", err)
			return
		}
		defer res.Body.Close()
		if res.IsError() && res.StatusCode != http.StatusNotFound {
			t.Errorf("delete integration index: status %d", res.StatusCode)
		}
	})
	res, err := (esapi.IndicesCreateRequest{
		Index: index,
		Body:  strings.NewReader(`{"mappings":{"properties":{"counter":{"type":"unsigned_long"}}}}`),
	}).Do(ctx, backend.transport)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.IsError() {
		t.Fatal(responseError("create integration index", index, res))
	}
	want := []string{"9007199254740992", "9007199254740993", "18446744073709551614", "18446744073709551615"}
	for i, key := range want {
		record := driver.Record{ID: strconv.Itoa(i), Data: []byte(`{"counter":` + key + `}`)}
		if err := backend.Save(ctx, record, driver.SaveOptions{WaitForVisibility: true}); err != nil {
			t.Fatal(err)
		}
	}
	values, err := backend.Values(ctx, "counter", driver.Query{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(values, want) {
		t.Fatalf("OpenSearch Values() = %q, want %q", values, want)
	}
}
