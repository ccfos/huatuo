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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/core/autotracing"
)

func TestNewProfileStoresRegistersElasticsearchAndPyroscope(t *testing.T) {
	elasticsearch := newElasticsearchInfoServer(t)
	cfg := &config.BamaiConfig{}
	cfg.Storage.Elasticsearch.Address = elasticsearch.URL
	cfg.Storage.Elasticsearch.Username = "elastic"
	cfg.Storage.Elasticsearch.Password = "secret"
	cfg.Storage.Elasticsearch.Index = "profiles"
	cfg.Storage.Pyroscope.Address = "http://127.0.0.1:4040"

	stores, err := newProfileStores(
		context.Background(),
		cfg,
		autotracing.DisplayBackendPyroscope,
	)
	if err != nil {
		t.Fatalf("newProfileStores returned error: %v", err)
	}
	defer func() {
		if err := closeProfileStores(context.Background(), stores); err != nil {
			t.Errorf("closeProfileStores returned error: %v", err)
		}
	}()

	if len(stores) != 2 {
		t.Fatalf("store count = %d, want 2", len(stores))
	}
	if stores[0].Name != "elasticsearch" {
		t.Errorf("stores[0].Name = %q, want elasticsearch", stores[0].Name)
	}
	if stores[1].Name != "pyroscope" {
		t.Errorf("stores[1].Name = %q, want pyroscope", stores[1].Name)
	}
}

func TestNewProfileStoresRejectsInvalidPyroscopeAuthentication(t *testing.T) {
	cfg := &config.BamaiConfig{}
	cfg.Storage.Pyroscope.Address = "http://127.0.0.1:4040"
	cfg.Storage.Pyroscope.Username = "user"

	if _, err := newProfileStores(
		context.Background(),
		cfg,
		autotracing.DisplayBackendPyroscope,
	); err == nil {
		t.Fatal("newProfileStores error = nil, want authentication error")
	}
}

func TestNewProfileStoresAPIServerModeSkipsPyroscope(t *testing.T) {
	elasticsearch := newElasticsearchInfoServer(t)
	cfg := &config.BamaiConfig{}
	cfg.Storage.Elasticsearch.Address = elasticsearch.URL
	cfg.Storage.Elasticsearch.Username = "elastic"
	cfg.Storage.Elasticsearch.Password = "secret"
	cfg.Storage.Elasticsearch.Index = "profiles"
	cfg.Storage.Pyroscope.Address = "http://127.0.0.1:4040"

	stores, err := newProfileStores(
		context.Background(),
		cfg,
		autotracing.DisplayBackendAPIServer,
	)
	if err != nil {
		t.Fatalf("newProfileStores returned error: %v", err)
	}
	defer func() {
		if err := closeProfileStores(context.Background(), stores); err != nil {
			t.Errorf("closeProfileStores returned error: %v", err)
		}
	}()

	if len(stores) != 1 || stores[0].Name != "elasticsearch" {
		t.Fatalf("stores = %#v, want only elasticsearch", stores)
	}
}

func newElasticsearchInfoServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			_, _ = w.Write([]byte(
				`{"name":"test","version":{"number":"7.17.0"}}`,
			))
		},
	))
	t.Cleanup(server.Close)
	return server
}

func TestValidateDisplayStorage(t *testing.T) {
	tests := []struct {
		name      string
		backend   autotracing.DisplayBackend
		configure func(*config.BamaiConfig)
		esEnabled bool
		want      string
	}{
		{
			name:    "Pyroscope address required",
			backend: autotracing.DisplayBackendPyroscope,
			want:    "Storage.Pyroscope.Address",
		},
		{
			name:    "API server Elasticsearch required",
			backend: autotracing.DisplayBackendAPIServer,
			want:    "Storage.Elasticsearch address, username, and password",
		},
		{
			name:    "Pyroscope configured",
			backend: autotracing.DisplayBackendPyroscope,
			configure: func(cfg *config.BamaiConfig) {
				cfg.Storage.Pyroscope.Address = "http://127.0.0.1:4040"
			},
		},
		{
			name:      "API server configured",
			backend:   autotracing.DisplayBackendAPIServer,
			esEnabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.BamaiConfig{}
			if tt.configure != nil {
				tt.configure(cfg)
			}
			err := validateDisplayStorage(cfg, tt.backend, tt.esEnabled)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateDisplayStorage returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf(
					"validateDisplayStorage error = %v, want %q",
					err,
					tt.want,
				)
			}
		})
	}
}
