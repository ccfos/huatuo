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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiv1 "github.com/ccfos/huatuo/apis/v1"
	nodeapi "github.com/ccfos/huatuo/apis/v1/node"
	"github.com/ccfos/huatuo/cmd/huatuo-bamai/config"
	"github.com/ccfos/huatuo/internal/server/response"
)

func TestUpdateConfigUpdatesTypedValues(t *testing.T) {
	if err := config.Load(writeConfig(t, "")); err != nil {
		t.Fatalf("load config: %v", err)
	}

	handler := &NodeAPIHandler{updateConfig: config.UpdateAndSync}
	got, err := handler.UpdateConfig(t.Context(), nodeapi.UpdateConfigRequestObject{
		Body: &nodeapi.UpdateConfigJSONRequestBody{
			Config: map[string]json.RawMessage{
				"BlackList":              json.RawMessage(`["dropwatch","netdev_hw"]`),
				"Runtime.CPULimitCores":  json.RawMessage(`1.5`),
				"Runtime.MemoryLimitMiB": json.RawMessage(`1024`),
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	if _, ok := got.(nodeapi.UpdateConfig204Response); !ok {
		t.Fatalf("UpdateConfig() response type = %T, want HTTP 204", got)
	}

	snapshot := config.Get()
	if snapshot.Runtime.CPULimitCores != 1.5 || snapshot.Runtime.MemoryLimitMiB != 1024 {
		t.Fatalf("Runtime = %+v, want typed numeric updates", snapshot.Runtime)
	}
	if got := snapshot.BlackList; len(got) != 2 || got[0] != "dropwatch" || got[1] != "netdev_hw" {
		t.Fatalf("BlackList = %v, want typed slice update", got)
	}
}

func TestUpdateConfigRejectsInvalidValues(t *testing.T) {
	if err := config.Load(writeConfig(t, "")); err != nil {
		t.Fatalf("load config: %v", err)
	}

	handler := &NodeAPIHandler{updateConfig: config.UpdateAndSync}
	tests := []struct {
		name  string
		key   string
		value json.RawMessage
	}{
		{name: "unknown key", key: "NotExist", value: json.RawMessage(`1`)},
		{name: "invalid type", key: "Runtime.MemoryLimitMiB", value: json.RawMessage(`"large"`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := handler.UpdateConfig(t.Context(), nodeapi.UpdateConfigRequestObject{
				Body: &nodeapi.UpdateConfigJSONRequestBody{
					Config: map[string]json.RawMessage{test.key: test.value},
				},
			})
			var apiErr *response.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("UpdateConfig() error = %v, want *response.APIError", err)
			}
			if apiErr.Code != apiv1.ErrorCodeInvalidRequest {
				t.Fatalf("UpdateConfig() error code = %q, want %q", apiErr.Code, apiv1.ErrorCodeInvalidRequest)
			}
			if !strings.Contains(apiErr.Message, test.key) {
				t.Fatalf("UpdateConfig() error message = %q, want key %q", apiErr.Message, test.key)
			}
		})
	}
}

func TestUpdateConfigHidesPersistenceFailure(t *testing.T) {
	handler := &NodeAPIHandler{
		updateConfig: func(map[string]any) error {
			return errors.New("disk path /secret/config is read-only")
		},
	}

	_, err := handler.UpdateConfig(t.Context(), nodeapi.UpdateConfigRequestObject{
		Body: &nodeapi.UpdateConfigJSONRequestBody{
			Config: map[string]json.RawMessage{
				"Runtime.MemoryLimitMiB": json.RawMessage(`1024`),
			},
		},
	})
	var apiErr *response.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("UpdateConfig() error = %v, want *response.APIError", err)
	}
	if apiErr.Code != apiv1.ErrorCodeInternal || apiErr.Message != "failed to persist config" {
		t.Fatalf("UpdateConfig() error = %+v, want sanitized internal error", apiErr)
	}
}

func TestUpdateConfigOpenAPIValidation(t *testing.T) {
	var updateCalls atomic.Int32
	handler := newTestNodeAPIHandler(t, time.Second)
	handler.updateConfig = func(map[string]any) error {
		updateCalls.Add(1)
		return nil
	}
	baseURL := startNodeAPIServer(t, handler)

	tests := []struct {
		name string
		body string
	}{
		{name: "missing config", body: `{}`},
		{name: "null config", body: `{"config":null}`},
		{name: "empty config", body: `{"config":{}}`},
		{name: "unknown top-level field", body: `{"config":{"Runtime.MemoryLimitMiB":1024},"extra":true}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(
				t.Context(),
				http.MethodPut,
				baseURL+"/v1/config",
				bytes.NewBufferString(test.body),
			)
			if err != nil {
				t.Fatalf("NewRequestWithContext() error = %v", err)
			}
			request.Header.Set("Authorization", "Bearer node-secret")
			request.Header.Set("Content-Type", "application/json")

			got, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("PUT /v1/config error = %v", err)
			}
			body, readErr := io.ReadAll(got.Body)
			if closeErr := got.Body.Close(); closeErr != nil {
				t.Errorf("close response body: %v", closeErr)
			}
			if readErr != nil {
				t.Fatalf("read response body: %v", readErr)
			}
			if got.StatusCode != http.StatusBadRequest {
				t.Fatalf("PUT /v1/config status = %d, want %d, body: %s",
					got.StatusCode, http.StatusBadRequest, body)
			}
			if !strings.Contains(string(body), `"code":"invalid_request"`) {
				t.Fatalf("PUT /v1/config body = %q, want invalid_request", body)
			}
		})
	}
	if got := updateCalls.Load(); got != 0 {
		t.Fatalf("update calls = %d, want 0", got)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := t.TempDir() + "/huatuo-bamai.conf"
	content += `

[HTTPServer.Auth]
BearerToken = "test-node-secret"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
