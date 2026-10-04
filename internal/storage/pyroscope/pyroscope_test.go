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
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
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
		wantName := "huatuo.profiler{tracer_id=trace-1,tracer_name=profiler," +
			"hostname=node-a,region=cn-hz,container_id=container-1," +
			"tracer_type=autotracing,profile_type=cpu}"
		if got := r.URL.Query().Get("name"); got != wantName {
			t.Errorf("name = %q, want %q", got, wantName)
		}
		if got := r.Header.Get("Content-Type"); got != protobufContentType {
			t.Errorf("Content-Type = %q, want %q", got, protobufContentType)
		}
		if got := r.Header.Get(authorizationHeaderName); got != "Bearer token-1" {
			t.Errorf("Authorization = %q, want bearer token", got)
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
	backend := newBackendWithHTTPClient(
		endpoint,
		"huatuo",
		"",
		"",
		"token-1",
		httpClient,
	)
	err := backend.Save(context.Background(), driver.Record{
		ID:   "trace-1",
		Data: profile,
		Fields: map[string]any{
			"profile_start_time": start,
			"profile_end_time":   end,
			"profile_type":       "cpu",
			"tracer_id":          "trace-1",
			"tracer_name":        "profiler",
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

func TestStorageSaveUsesBasicAuthentication(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "user" || password != "secret" {
			t.Errorf("BasicAuth = (%q, %q, %v)", username, password, ok)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})}
	endpoint, _ := url.Parse("https://pyroscope.test/ingest")
	backend := newBackendWithHTTPClient(endpoint, "huatuo", "user", "secret", "", httpClient)
	if err := backend.Save(context.Background(), driver.Record{Data: []byte{1}}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
}

func TestStorageSaveClosesErrorResponse(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("ingest failed")}
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       body,
			Header:     make(http.Header),
		}, nil
	})}
	endpoint, _ := url.Parse("http://pyroscope.test/ingest")
	backend := newBackendWithHTTPClient(endpoint, "huatuo", "", "", "", httpClient)
	err := backend.Save(context.Background(), driver.Record{Data: []byte{1}})
	if err == nil || !strings.Contains(err.Error(), "status 500: ingest failed") {
		t.Fatalf("Save error = %v, want status and response", err)
	}
	if !body.closed.Load() {
		t.Fatal("response body was not closed")
	}
}

func TestStorageSaveHonorsContext(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	endpoint, _ := url.Parse("http://pyroscope.test/ingest")
	backend := newBackendWithHTTPClient(endpoint, "huatuo", "", "", "", httpClient)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := backend.Save(ctx, driver.Record{Data: []byte{1}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Save error = %v, want context.Canceled", err)
	}
}

func TestNewBackendRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
	}{
		{name: "nil config", cfg: nil},
		{name: "empty address", cfg: &Config{}},
		{name: "missing scheme", cfg: &Config{Address: "127.0.0.1:4040"}},
		{name: "unsupported scheme", cfg: &Config{Address: "ftp://127.0.0.1"}},
		{name: "URL credentials", cfg: &Config{Address: "http://user:secret@127.0.0.1"}},
		{name: "query", cfg: &Config{Address: "http://127.0.0.1?tenant=a"}},
		{name: "fragment", cfg: &Config{Address: "http://127.0.0.1#fragment"}},
		{
			name: "incomplete basic authentication",
			cfg:  &Config{Address: "http://127.0.0.1", Username: "user"},
		},
		{
			name: "colon in basic authentication username",
			cfg: &Config{
				Address:  "http://127.0.0.1",
				Username: "user:name",
				Password: "secret",
			},
		},
		{
			name: "multiple authentication methods",
			cfg: &Config{
				Address:     "http://127.0.0.1",
				Username:    "user",
				Password:    "secret",
				BearerToken: "token",
			},
		},
		{
			name: "control character in token",
			cfg:  &Config{Address: "http://127.0.0.1", BearerToken: "a\nb"},
		},
		{
			name: "surrounding whitespace in token",
			cfg:  &Config{Address: "http://127.0.0.1", BearerToken: " token"},
		},
		{
			name: "negative timeout",
			cfg:  &Config{Address: "http://127.0.0.1", TimeoutSeconds: -1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewBackend(tt.cfg); err == nil {
				t.Fatalf("NewBackend(%#v) error = nil", tt.cfg)
			}
		})
	}
}

func TestStorageDoesNotFollowRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	backend, err := NewBackend(&Config{Address: source.URL})
	if err != nil {
		t.Fatalf("NewBackend returned error: %v", err)
	}
	err = backend.Save(context.Background(), driver.Record{Data: []byte{1}})
	if err == nil || !strings.Contains(err.Error(), "status 307") {
		t.Fatalf("Save error = %v, want redirect status", err)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests.Load())
	}
}

func TestStorageReadOperationsAreUnsupported(t *testing.T) {
	backend, err := NewBackend(&Config{Address: "http://127.0.0.1:4040"})
	if err != nil {
		t.Fatalf("NewBackend returned error: %v", err)
	}
	if _, err := backend.Get(context.Background(), "id"); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("Get error = %v, want ErrUnsupportedOp", err)
	}
	if err := backend.Delete(context.Background(), "id"); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("Delete error = %v, want ErrUnsupportedOp", err)
	}
	if _, err := backend.Query(context.Background(), driver.Query{}); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("Query error = %v, want ErrUnsupportedOp", err)
	}
	if _, err := backend.Count(context.Background(), driver.Query{}); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("Count error = %v, want ErrUnsupportedOp", err)
	}
	if _, err := backend.Values(context.Background(), "field", driver.Query{}, 1); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("Values error = %v, want ErrUnsupportedOp", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingReadCloser struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackingReadCloser) Close() error {
	b.closed.Store(true)
	return nil
}
