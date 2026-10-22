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

	"github.com/ccfos/huatuo/internal/mcp/backend/escompatible"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/mcp/utils"
)

func TestEventFactorySelectsCompatibleReadAdaptersWithoutProbes(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { attempts.Add(1); _, _ = w.Write([]byte(`{"count":1}`)) }))
	defer server.Close()
	for _, kind := range []string{"", constant.EventBackendElasticsearch, constant.EventBackendOpenSearch} {
		reader, err := NewEventReader(&EventConfig{Type: kind, ESCompatible: escompatible.Connection{Address: server.URL, Index: "events"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		expected := kind
		if expected == "" {
			expected = constant.EventBackendElasticsearch
		}
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
	if _, err := NewEventReader(&EventConfig{Type: "unknown"}, nil); err == nil {
		t.Fatal("unknown backend accepted")
	}
}

func TestResolveEventConfigInheritsOnlyCompatibleConnection(t *testing.T) {
	defaults := EventConfig{Type: constant.EventBackendElasticsearch, ESCompatible: escompatible.Connection{
		Address: "http://original:9200", Index: "events", Username: "private", Password: "secret",
	}}
	input := EventConfig{Type: " OpenSearch ", ESCompatible: escompatible.Connection{Address: "http://anonymous:9200", AuthType: "none"}}
	resolved, err := ResolveEventConfig(&input, &defaults)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Type != constant.EventBackendOpenSearch || resolved.ESCompatible.Index != "events" || resolved.ESCompatible.Username != "" || resolved.ESCompatible.Password != "" {
		t.Fatalf("unexpected resolved connection: %+v", resolved)
	}
	if input.ESCompatible.Index != "" || defaults.ESCompatible.Username != "private" {
		t.Fatal("resolution mutated decoded configuration")
	}
	input.Type = "clickhouse"
	if _, err := ResolveEventConfig(&input, &defaults); err == nil {
		t.Fatal("unsupported adapter accepted ES-compatible defaults")
	}
}

func TestEventFactoryChargesSharedBudgetWithSelectedIdentity(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"count":1,"fields":{"region":{"keyword":{"searchable":true,"aggregatable":true}},"tracer_type":{"keyword":{"searchable":true,"aggregatable":true}},"uploaded_time":{"date":{"searchable":true,"aggregatable":true}}}}`))
	}))
	defer server.Close()
	limiter := utils.NewRateLimiter(2, 0)
	var rejectedKind string
	options := EventRuntimeOptions{BeforeRequest: func(ctx context.Context, kind string) error {
		err := limiter.AllowBackendRequest(ctx)
		if errors.Is(err, utils.ErrRateLimitExceeded) {
			rejectedKind = kind
		}
		return err
	}}
	for _, kind := range []string{constant.EventBackendElasticsearch, constant.EventBackendOpenSearch} {
		reader, err := NewEventReader(&EventConfig{Type: kind, ESCompatible: escompatible.Connection{Address: server.URL, Index: "events"}}, &options)
		if err != nil {
			t.Fatal(err)
		}
		_, err = reader.Count(t.Context(), &event.Query{Region: "cn"})
		_ = reader.Close(t.Context())
		if kind == constant.EventBackendElasticsearch && err != nil {
			t.Fatal(err)
		}
		if kind == constant.EventBackendOpenSearch && !errors.Is(err, utils.ErrRateLimitExceeded) {
			t.Fatalf("second adapter bypassed shared budget: %v", err)
		}
	}
	if rejectedKind != constant.EventBackendOpenSearch || requests.Load() != 2 {
		t.Fatalf("rejected kind=%q HTTP requests=%d", rejectedKind, requests.Load())
	}
}
