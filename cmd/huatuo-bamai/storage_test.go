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
	"testing"

	"huatuo-bamai/cmd/huatuo-bamai/config"
)

func TestNewProfileStoresRegistersElasticsearchAndPyroscope(t *testing.T) {
	cfg := &config.BamaiConfig{}
	cfg.Storage.ES.Address = "http://127.0.0.1:9200"
	cfg.Storage.ES.Username = "elastic"
	cfg.Storage.ES.Password = "secret"
	cfg.Storage.ES.Index = "profiles"
	cfg.Storage.Pyroscope.Address = "http://127.0.0.1:4040"

	stores, err := newProfileStores(context.Background(), cfg)
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

	if _, err := newProfileStores(context.Background(), cfg); err == nil {
		t.Fatal("newProfileStores error = nil, want authentication error")
	}
}
