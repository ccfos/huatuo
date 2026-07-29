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
	if !strings.Contains(rec.Body.String(), "invalid elem") {
		t.Fatalf("body = %q, want invalid elem error", rec.Body.String())
	}
}

func TestConfigHandlerUpdatesNumericConfig(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	if err := config.Load(writeConfig(t, `
[Runtime]
StartupCPULimitCores = 0.5
CPULimitCores = 2.0
MemoryLimitMiB = 2048

[Pod]
KubeletReadOnlyPort = 10255
`)); err != nil {
		t.Fatalf("load config: %v", err)
	}

	engine := httpGin.New()
	server.NewRoot(engine, "").PUT("/config", NewConfigHandler().update)

	body := `{"config":{
		"Runtime.StartupCPULimitCores":0.75,
		"Runtime.CPULimitCores":1.5,
		"Runtime.MemoryLimitMiB":1024,
		"Pod.KubeletReadOnlyPort":10250
	}}`
	req := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	cfg := config.Get()
	if cfg.Runtime.StartupCPULimitCores != 0.75 {
		t.Errorf("StartupCPULimitCores = %v, want 0.75", cfg.Runtime.StartupCPULimitCores)
	}
	if cfg.Runtime.CPULimitCores != 1.5 {
		t.Errorf("CPULimitCores = %v, want 1.5", cfg.Runtime.CPULimitCores)
	}
	if cfg.Runtime.MemoryLimitMiB != 1024 {
		t.Errorf("MemoryLimitMiB = %d, want 1024", cfg.Runtime.MemoryLimitMiB)
	}
	if cfg.Pod.KubeletReadOnlyPort != 10250 {
		t.Errorf("KubeletReadOnlyPort = %d, want 10250", cfg.Pod.KubeletReadOnlyPort)
	}
}

func TestConfigHandlerRejectsFractionalInteger(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	if err := config.Load(writeConfig(t, `
[Runtime]
StartupCPULimitCores = 0.5
CPULimitCores = 2.0
MemoryLimitMiB = 2048
`)); err != nil {
		t.Fatalf("load config: %v", err)
	}

	engine := httpGin.New()
	server.NewRoot(engine, "").PUT("/config", NewConfigHandler().update)

	req := httptest.NewRequest(
		http.MethodPut,
		"/config",
		bytes.NewBufferString(`{"config":{"Runtime.MemoryLimitMiB":1024.5}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if config.Get().Runtime.MemoryLimitMiB != 2048 {
		t.Errorf("MemoryLimitMiB changed to %d after rejected update", config.Get().Runtime.MemoryLimitMiB)
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
