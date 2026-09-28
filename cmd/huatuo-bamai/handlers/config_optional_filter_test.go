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

package handlers

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	internalconfig "github.com/ccfos/huatuo/internal/config"
)

func TestUpdateConfigOptionalFilter(t *testing.T) {
	valid := `{"config":{"AutoTracing.CPUIdle.Filter.Included":[{"Field":"container_host_namespace","Pattern":"^application-"}]}}`
	tests := []struct {
		name       string
		initial    string
		body       string
		wantStatus int
	}{
		{name: "default filter", body: valid, wantStatus: http.StatusNoContent},
		{
			name: "existing filter", body: valid, wantStatus: http.StatusNoContent,
			initial: "[[AutoTracing.CPUIdle.Filter.Excluded]]\nField = \"container_qos\"\nPattern = \"besteffort\"\n",
		},
		{
			name: "invalid runtime after filter", wantStatus: http.StatusBadRequest,
			body: `{"config":{"AutoTracing.CPUIdle.Filter.Included":[{"Field":"container_host_namespace","Pattern":"^application-"}],"Runtime.CPULimitCores":-1}}`,
		},
		{
			name: "unknown nested field", wantStatus: http.StatusBadRequest,
			body: `{"config":{"AutoTracing.CPUIdle.Filter.Unknown":true}}`,
		},
		{
			name: "invalid nested type", wantStatus: http.StatusBadRequest,
			body: `{"config":{"AutoTracing.CPUIdle.Filter.Included":"invalid"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.initial)
			if err := config.Load(path); err != nil {
				t.Fatal(err)
			}
			beforeFile, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			before := config.Get()
			beforeCopy := before.Clone()
			handler := newTestNodeAPIHandler(t, time.Second)
			handler.updateConfig = config.UpdateAndSync
			baseURL := startNodeAPIServer(t, handler)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPut, baseURL+"/v1/config", bytes.NewBufferString(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer node-secret")
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tt.wantStatus {
				t.Fatalf("PUT /v1/config status = %d, want %d: %s", response.StatusCode, tt.wantStatus, body)
			}
			if !reflect.DeepEqual(before, beforeCopy) {
				t.Fatal("update mutated the previously published snapshot")
			}
			if tt.wantStatus != http.StatusNoContent {
				if config.Get() != before {
					t.Fatal("rejected update published a new snapshot")
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, beforeFile) {
					t.Fatal("rejected update changed the persisted config")
				}
				return
			}
			var persisted config.Config
			if err := internalconfig.Load(path, &persisted); err != nil {
				t.Fatalf("reload persisted config: %v", err)
			}
			for _, cfg := range []*config.Config{config.Get(), &persisted} {
				filter := cfg.AutoTracing.CPUIdle.Filter
				if filter == nil || len(filter.Included) != 1 || filter.Included[0].Pattern != "^application-" {
					t.Fatalf("filter was not applied and persisted: %+v", filter)
				}
				if tt.initial != "" && (len(filter.Excluded) != 1 || filter.Excluded[0].Pattern != "besteffort") {
					t.Fatalf("existing exclusions changed: %+v", filter.Excluded)
				}
			}
		})
	}
}
