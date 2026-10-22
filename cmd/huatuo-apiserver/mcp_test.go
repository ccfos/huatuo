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

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ccfos/huatuo/cmd/huatuo-apiserver/config"
	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/mcp/backend"
	"github.com/ccfos/huatuo/internal/mcp/backend/escompatible"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
)

func TestNewMCPEventStoreDoesNotProbeBackend(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	store, err := backend.NewEventReader(&backend.EventConfig{ESCompatible: escompatible.Connection{
		Address:  server.URL,
		Username: "mcp-user",
		Password: "mcp-password",
		Index:    "huatuo_bamai",
	}}, nil)
	if err != nil {
		t.Fatalf("newMCPEventStore() error = %v", err)
	}
	defer func() {
		if err := store.Close(t.Context()); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()
	if requests.Load() != 0 {
		t.Fatalf("startup backend requests = %d, want 0", requests.Load())
	}
	if _, err := store.TracerNames(t.Context(), &event.Query{Region: "test-cn", Limit: 1}); err == nil {
		t.Fatal("TracerNames() error = nil, want unavailable backend error")
	}
	if requests.Load() == 0 {
		t.Fatal("TracerNames() made no backend request")
	}
}

func TestNewMCPEventStoresKeepsApplicationConnectionIsolated(t *testing.T) {
	var applicationRequests atomic.Int64
	application := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		applicationRequests.Add(1)
		_, _ = w.Write([]byte(`{"count":0}`))
	}))
	defer application.Close()
	applicationConfig := internalconfig.ElasticsearchConfig{Address: application.URL, Index: "application-events", Username: "application-user", Password: "application-password"}
	daemon := &Daemon{opts: &Options{Config: &config.Config{Elasticsearch: applicationConfig}}}
	reader, regions, owned, err := newMCPEventStores(t.Context(), daemon, nil)
	if err != nil || reader != nil || len(regions) != 0 || len(owned) != 0 {
		t.Fatalf("unconfigured MCP reader inherited application ES: reader=%v regions=%v owned=%v error=%v", reader, regions, owned, err)
	}
	for _, username := range []string{"", "mcp-user"} {
		t.Run("username="+username, func(t *testing.T) {
			type observedRequest struct{ path, user, password string }
			requests := make(chan observedRequest, 2)
			mcpEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, _ := r.BasicAuth()
				if strings.HasSuffix(r.URL.Path, "/_field_caps") {
					if user != username {
						t.Errorf("metadata inherited application credentials")
					}
					_, _ = w.Write([]byte(`{"fields":{"region":{"keyword":{"searchable":true,"aggregatable":true}},"tracer_type":{"keyword":{"searchable":true,"aggregatable":true}},"uploaded_time":{"date":{"searchable":true,"aggregatable":true}}}}`))
					return
				}
				requests <- observedRequest{r.URL.Path, user, password}
				_, _ = w.Write([]byte(`{"count":0}`))
			}))
			defer mcpEndpoint.Close()
			connection := escompatible.Connection{Address: mcpEndpoint.URL, Index: "mcp-events", Username: username}
			if username != "" {
				connection.Password = "mcp-password"
			}
			daemon := &Daemon{opts: &Options{Config: &config.Config{
				Elasticsearch: applicationConfig,
				MCP: config.MCPConfig{Events: config.MCPEventsConfig{
					Type: "opensearch", ESCompatible: connection,
					Regions: []config.MCPEventRegionConfig{{NamePattern: "us", ESCompatible: escompatible.Connection{Index: "mcp-region-events"}}},
				}},
			}}}
			reader, regions, owned, err := newMCPEventStores(t.Context(), daemon, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				for _, reader := range owned {
					_ = reader.Close(t.Context())
				}
			}()
			if len(requests) != 0 {
				t.Fatal("MCP setup probed backend")
			}
			if reader == nil || len(regions) != 1 {
				t.Fatalf("missing MCP readers: %v", regions)
			}
			for _, reader := range []event.Reader{reader, regions[0].Store} {
				if reader.BackendType() != "opensearch" {
					t.Fatalf("region did not inherit MCP adapter identity: %s", reader.BackendType())
				}
				if _, err := reader.Count(t.Context(), &event.Query{Region: "us"}); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{"/mcp-events/_count", "/mcp-region-events/_count"} {
				request := <-requests
				if request.path != path || request.user != connection.Username || request.password != connection.Password {
					t.Fatalf("MCP reader inherited application settings or lost internal inheritance: %+v", request)
				}
			}
		})
	}
	if applicationRequests.Load() != 0 {
		t.Fatalf("MCP contacted application backend %d times", applicationRequests.Load())
	}
}

func TestMCPAnonymousEndpointDoesNotReceiveInheritedCredentials(t *testing.T) {
	var credentials atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credentials.Store(r.Header.Get("Authorization") != "")
		if strings.HasSuffix(r.URL.Path, "/_field_caps") {
			_, _ = w.Write([]byte(`{"fields":{"region":{"keyword":{"searchable":true,"aggregatable":true}},"tracer_type":{"keyword":{"searchable":true,"aggregatable":true}},"tracer_name":{"keyword":{"searchable":true,"aggregatable":true}},"uploaded_time":{"date":{"searchable":true,"aggregatable":true}}}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"aggregations":{"sterms#terms":{"buckets":[]}}}`))
	}))
	defer server.Close()
	fallback := backend.EventConfig{Type: "elasticsearch", ESCompatible: escompatible.Connection{
		Address: "http://original:9200", Index: "events", Username: "private-user", Password: "private-pass",
	}}
	input := backend.EventConfig{ESCompatible: escompatible.Connection{Address: server.URL, AuthType: "none"}}
	cfg, err := backend.ResolveEventConfig(&input, &fallback)
	if err != nil {
		t.Fatal(err)
	}
	store, err := backend.NewEventReader(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close(t.Context()) }()
	if _, err := store.TracerNames(t.Context(), &event.Query{Region: "cn", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if credentials.Load() {
		t.Fatal("inherited credentials sent to anonymous endpoint")
	}
}
