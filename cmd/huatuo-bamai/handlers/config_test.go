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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"huatuo-bamai/cmd/huatuo-bamai/config"
	"huatuo-bamai/internal/server"

	httpGin "github.com/gin-gonic/gin"
)

func TestConfigHandlerRejectsInvalidConfigKey(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	if err := config.Load(writeConfig(t, `
Log = { Level = "Info" }
`)); err != nil {
		t.Fatalf("load config: %v", err)
	}

	engine := httpGin.New()
	server.NewRoot(engine, "").PUT("/config", NewConfigHandler().update)

	req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(`{"config":{"NotExist":1}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does not exist") {
		t.Fatalf("body = %q, want invalid field error", rec.Body.String())
	}
}

func TestConfigHandlerUpdatesTypedValues(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	if err := config.Load(writeConfig(t, "")); err != nil {
		t.Fatalf("load config: %v", err)
	}

	engine := httpGin.New()
	server.NewRoot(engine, "").PUT("/config", NewConfigHandler().update)
	req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(`{
		"config": {
			"BlackList": ["dropwatch", "netdev_hw"],
			"Runtime.CPULimitCores": 1.5,
			"Runtime.MemoryLimitMiB": 1024
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	snapshot := config.Get()
	if snapshot.Runtime.CPULimitCores != 1.5 || snapshot.Runtime.MemoryLimitMiB != 1024 {
		t.Fatalf("Runtime = %+v, want typed numeric updates", snapshot.Runtime)
	}
	if got := snapshot.BlackList; len(got) != 2 || got[0] != "dropwatch" || got[1] != "netdev_hw" {
		t.Fatalf("BlackList = %v, want typed slice update", got)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := t.TempDir() + "/huatuo-bamai.conf"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
