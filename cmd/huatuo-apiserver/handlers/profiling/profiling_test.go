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

package profiling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/server"

	"github.com/gin-gonic/gin"
)

func TestHandlerRegistersCapabilitiesRouteBeforeIDRoute(t *testing.T) {
	h := NewHandler(nil)

	capabilitiesIdx := -1
	idIdx := -1
	for i, route := range h.Handlers {
		if route.Typ == server.HttpGet && route.Uri == "/capabilities" {
			capabilitiesIdx = i
		}
		if route.Typ == server.HttpGet && route.Uri == "/:id" {
			idIdx = i
		}
	}

	if capabilitiesIdx == -1 {
		t.Fatalf("GET /capabilities route was not registered")
	}
	if idIdx == -1 {
		t.Fatalf("GET /:id route was not registered")
	}
	if capabilitiesIdx > idIdx {
		t.Fatalf("GET /capabilities route index=%d, want before GET /:id index=%d", capabilitiesIdx, idIdx)
	}
}

func TestCapabilities(t *testing.T) {
	cfg := config.Get()
	oldProfilingConfig := cfg.Profiling
	cfg.Profiling.CPUProfilingInterval = 11
	cfg.Profiling.MemoryProfilingInterval = 12
	cfg.Profiling.CPUSingleTraceTimeout = 21
	cfg.Profiling.MemorySingleTraceTimeout = 22
	cfg.Profiling.ThirdPartyToolLimit = 7
	defer func() {
		cfg.Profiling = oldProfilingConfig
	}()

	engine := newProfilingTestEngine(t)
	request := httptest.NewRequest(http.MethodGet, "/v1/profiles/capabilities", http.NoBody)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var got struct {
		Code    int                              `json:"code"`
		Message string                           `json:"message"`
		Data    v1.ProfilingCapabilitiesResponse `json:"data"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if got.Code != 0 {
		t.Errorf("response code = %d, want 0", got.Code)
	}
	if got.Message != "success" {
		t.Errorf("response message = %q, want %q", got.Message, "success")
	}

	want := v1.ProfilingCapabilitiesResponse{
		Types:           []string{"cpu", "memory"},
		CPULanguages:    []string{"c", "c++", "go", "java", "python"},
		MemoryLanguages: []string{"c", "c++", "go", "java"},
		MemoryModes: []string{
			"NATIVE_PHYSICAL_ALLOC",
			"NATIVE_PHYSICAL_USAGE",
			"NATIVE_VIRTUAL_ALLOC",
			"OBJECT_ALLOC",
			"OBJECT_USAGE",
		},
		Defaults: v1.ProfilingCapabilityDefaults{
			CPUProfilingInterval:     11,
			MemoryProfilingInterval:  12,
			CPUSingleTraceTimeout:    21,
			MemorySingleTraceTimeout: 22,
			ThirdPartyToolLimit:      7,
		},
	}

	if !reflect.DeepEqual(got.Data, want) {
		t.Errorf("capabilities response = %+v, want %+v", got.Data, want)
	}
}

func newProfilingTestEngine(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := server.NewRoot(engine, "").Group("/v1/profiles")
	for _, route := range NewHandler(nil).Handlers {
		method, ok := testHTTPMethod(route.Typ)
		if !ok {
			t.Fatalf("unknown route type: %d", route.Typ)
		}
		group.Handle(method, route.Uri, route.Handle)
	}

	return engine
}

func testHTTPMethod(typ int) (string, bool) {
	switch typ {
	case server.HttpPost:
		return http.MethodPost, true
	case server.HttpDelete:
		return http.MethodDelete, true
	case server.HttpGet:
		return http.MethodGet, true
	case server.HttpPut:
		return http.MethodPut, true
	case server.HttpPatch:
		return http.MethodPatch, true
	default:
		return "", false
	}
}
