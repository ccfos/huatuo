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

package pyroscope

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"huatuo-bamai/internal/storage/driver"
)

func TestStorageSaveIngestsPprof(t *testing.T) {
	profile := []byte{0x0a, 0x01, 0x00}
	start := time.Unix(1700000000, 0).UTC()
	end := start.Add(10 * time.Second)

	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/base/ingest" {
			t.Errorf("path = %s, want /base/ingest", r.URL.Path)
		}
		if got := r.URL.Query().Get("format"); got != pprofFormat {
			t.Errorf("format = %q, want %q", got, pprofFormat)
		}
		if got := r.URL.Query().Get("from"); got != strconv.FormatInt(start.Unix(), 10) {
			t.Errorf("from = %q, want %d", got, start.Unix())
		}
		if got := r.URL.Query().Get("until"); got != strconv.FormatInt(end.Unix(), 10) {
			t.Errorf("until = %q, want %d", got, end.Unix())
		}
		wantName := "huatuo.cpuidle{tracer_id=trace-1,tracer_name=cpuidle,hostname=node-a,region=cn-hz,container_id=container-1,tracer_type=autotracing}"
		if got := r.URL.Query().Get("name"); got != wantName {
			t.Errorf("name = %q, want %q", got, wantName)
		}
		if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("Content-Type = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !bytes.Equal(body, profile) {
			t.Errorf("body = %v, want %v", body, profile)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})}
	endpoint, _ := url.Parse("http://pyroscope.test/base/ingest")
	backend := newBackendWithHTTPClient(endpoint, "huatuo", httpClient)
	err := backend.Save(context.Background(), driver.Record{
		ID:   "trace-1",
		Data: profile,
		Fields: map[string]any{
			"profile_start_time": start,
			"profile_end_time":   end,
			"tracer_id":          "trace-1",
			"tracer_name":        "cpuidle",
			"hostname":           "node-a",
			"region":             "cn-hz",
			"container_id":       "container-1",
			"tracer_type":        "autotracing",
		},
	})
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
}

func TestStorageSaveReturnsHTTPError(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(bytes.NewBufferString("ingest failed")),
			Header:     make(http.Header),
		}, nil
	})}
	endpoint, _ := url.Parse("http://pyroscope.test/ingest")
	backend := newBackendWithHTTPClient(endpoint, "huatuo", httpClient)
	if err := backend.Save(context.Background(), driver.Record{Data: []byte{1}}); err == nil {
		t.Fatal("Save error = nil, want HTTP error")
	}
}

func TestNewBackendRejectsInvalidConfig(t *testing.T) {
	for _, cfg := range []*Config{nil, {}, {Address: "127.0.0.1:4040"}} {
		if _, err := NewBackend(cfg); err == nil {
			t.Fatalf("NewBackend(%#v) error = nil", cfg)
		}
	}
}

func TestStorageReadOperationsAreUnsupported(t *testing.T) {
	backend, err := NewBackend(&Config{Address: "http://127.0.0.1:4040"})
	if err != nil {
		t.Fatalf("NewBackend returned error: %v", err)
	}
	if _, err := backend.Get(context.Background(), "id"); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("Get error = %v", err)
	}
	if err := backend.Delete(context.Background(), "id"); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("Delete error = %v", err)
	}
	if _, err := backend.Query(context.Background(), driver.Query{}); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("Query error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
