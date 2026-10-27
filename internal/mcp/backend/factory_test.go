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

package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/backend/storage"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

func TestEventFactoryCreatesCompatibleReaderWithoutProbes(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { attempts.Add(1); _, _ = w.Write([]byte(`{"count":1}`)) }))
	defer server.Close()
	for range 2 {
		reader, err := NewEventReader(&EventConfig{Storage: storage.Connection{Address: server.URL, Index: "events"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		expected := event.EventBackendElasticsearch
		if reader.BackendType() != expected {
			t.Fatalf("backend=%s want=%s", reader.BackendType(), expected)
		}
		if _, ok := reader.(event.TracerCounter); !ok {
			t.Fatal("count aggregation capability missing")
		}
		_ = reader.Close(t.Context())
	}
	if attempts.Load() != 0 {
		t.Fatal("factory probed backend")
	}
	if _, err := NewEventReader(nil, nil); err == nil {
		t.Fatal("missing connection accepted")
	}
}

func TestResolveEventConfigNormalizesIndependentConnection(t *testing.T) {
	input := EventConfig{Storage: storage.Connection{Address: "http://anonymous:9200", Index: "events", AuthType: "none", Username: "private", Password: "secret"}}
	resolved, err := ResolveEventConfig(&input)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Storage.Index != "events" || resolved.Storage.Username != "" || resolved.Storage.Password != "" {
		t.Fatalf("unexpected resolved connection: %+v", resolved)
	}
	if input.Storage.Username != "private" {
		t.Fatal("resolution mutated decoded configuration")
	}
	input.Storage.Index = ""
	if _, err := ResolveEventConfig(&input); err == nil {
		t.Fatal("missing index accepted")
	}
}

func TestEventFactoryChargesSharedBudgetAcrossReaders(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"count":1,"fields":{"region":{"keyword":{"searchable":true,"aggregatable":true}},"tracer_type":{"keyword":{"searchable":true,"aggregatable":true}},"uploaded_time":{"date":{"searchable":true,"aggregatable":true}}}}`))
	}))
	defer server.Close()
	limiter := utils.NewRateLimiter(2)
	var rejectedKind string
	options := EventRuntimeOptions{BeforeRequest: func(ctx context.Context, kind string) error {
		err := limiter.AllowBackendRequest(ctx)
		if errors.Is(err, utils.ErrRateLimitExceeded) {
			rejectedKind = kind
		}
		return err
	}}
	for attempt := range 2 {
		reader, err := NewEventReader(&EventConfig{Storage: storage.Connection{Address: server.URL, Index: "events"}}, &options)
		if err != nil {
			t.Fatal(err)
		}
		_, err = reader.Count(t.Context(), &event.Query{Region: "cn"})
		_ = reader.Close(t.Context())
		if attempt == 0 && err != nil {
			t.Fatal(err)
		}
		if attempt == 1 && !errors.Is(err, utils.ErrRateLimitExceeded) {
			t.Fatalf("second reader bypassed shared budget: %v", err)
		}
	}
	if rejectedKind != event.EventBackendElasticsearch || requests.Load() != 2 {
		t.Fatalf("rejected kind=%q HTTP requests=%d", rejectedKind, requests.Load())
	}
}
