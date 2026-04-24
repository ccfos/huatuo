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

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpGin "github.com/gin-gonic/gin"
	httpBinding "github.com/gin-gonic/gin/binding"
)

func newTestServerContext(method, target, body string) (*Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := httpGin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		ginCtx.Request.Header.Set("Content-Type", "application/json")
	}
	return newContext(ginCtx), recorder
}

// TestContextLifecycleAndAccessors 覆盖 server.Context 的核心行为，验证了上下文注入、缺失上下文时回补、路径和查询参数读取、JSON 和 Query 绑定、默认查询参数、Header/JSON 输出，以及 Request/Writer 访问器都能正常工作。
func TestContextLifecycleAndAccessors(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := httpGin.CreateTestContext(recorder)

	want := newContext(ginCtx)
	got := internalContext(ginCtx)
	if got != want {
		t.Errorf("internalContext() returned %p, want %p", got, want)
	}

	fallbackRecorder := httptest.NewRecorder()
	fallbackGinCtx, _ := httpGin.CreateTestContext(fallbackRecorder)
	fallbackGinCtx.Set(contextKey, "trace-2026")
	fallbackCtx := internalContext(fallbackGinCtx)
	if fallbackCtx == nil {
		t.Errorf("internalContext() returned nil for invalid stored context")
	} else if fallbackCtx.c != fallbackGinCtx {
		t.Errorf("fallback context wrapped gin.Context %p, want %p", fallbackCtx.c, fallbackGinCtx)
	}

	httpGin.SetMode(httpGin.TestMode)
	engine := httpGin.New()
	engine.Use(middlewareContext())

	var middlewareParam string
	engine.GET("/tasks/:id", func(c *httpGin.Context) {
		middlewareParam = internalContext(c).Param("id")
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/tasks/task-20250226", http.NoBody)
	middlewareRecorder := httptest.NewRecorder()
	engine.ServeHTTP(middlewareRecorder, request)

	if middlewareRecorder.Code != http.StatusNoContent {
		t.Errorf("middleware response status = %d, want %d", middlewareRecorder.Code, http.StatusNoContent)
	}
	if middlewareParam != "task-20250226" {
		t.Errorf("Param(id) from middleware = %q, want %q", middlewareParam, "task-20250226")
	}

	ctx, jsonRecorder := newTestServerContext(
		http.MethodPost,
		"/tasks/task-20250226?view=full&limit=5",
		`{"hostname":"huatuo-dev","region":"huatuo-region"}`,
	)
	ctx.c.Params = httpGin.Params{{Key: "id", Value: "task-20250226"}}

	var payload struct {
		Hostname string `json:"hostname"`
		Region   string `json:"region"`
	}
	if err := ctx.ShouldBindJSON(&payload); err != nil {
		t.Errorf("ShouldBindJSON() error = %v", err)
	}
	if payload.Hostname != "huatuo-dev" {
		t.Errorf("Hostname = %q, want %q", payload.Hostname, "huatuo-dev")
	}
	if payload.Region != "huatuo-region" {
		t.Errorf("Region = %q, want %q", payload.Region, "huatuo-region")
	}

	var query struct {
		Limit int    `form:"limit"`
		View  string `form:"view"`
	}
	if err := ctx.ShouldBindQuery(&query); err != nil {
		t.Errorf("ShouldBindQuery() error = %v", err)
	}
	if query.Limit != 5 {
		t.Errorf("query limit = %d, want %d", query.Limit, 5)
	}
	if query.View != "full" {
		t.Errorf("query view = %q, want %q", query.View, "full")
	}

	bodyCtx, _ := newTestServerContext(
		http.MethodPost,
		"/tasks",
		`{"hostname":"huatuo-dev","region":"huatuo-region"}`,
	)
	var bodyPayload struct {
		Hostname string `json:"hostname"`
		Region   string `json:"region"`
	}
	if err := bodyCtx.ShouldBindBodyWith(&bodyPayload, httpBinding.JSON); err != nil {
		t.Errorf("ShouldBindBodyWith() error = %v", err)
	}
	if bodyPayload.Hostname != "huatuo-dev" {
		t.Errorf("body hostname = %q, want %q", bodyPayload.Hostname, "huatuo-dev")
	}
	if bodyPayload.Region != "huatuo-region" {
		t.Errorf("body region = %q, want %q", bodyPayload.Region, "huatuo-region")
	}

	if got := ctx.Param("id"); got != "task-20250226" {
		t.Errorf("Param(id) = %q, want %q", got, "task-20250226")
	}
	if got := ctx.Query("view"); got != "full" {
		t.Errorf("Query(view) = %q, want %q", got, "full")
	}
	if got := ctx.DefaultQuery("mode", "summary"); got != "summary" {
		t.Errorf("DefaultQuery(mode) = %q, want %q", got, "summary")
	}
	if ctx.Request() != ctx.c.Request {
		t.Errorf("Request() returned unexpected pointer")
	}
	if ctx.Writer() != ctx.c.Writer {
		t.Errorf("Writer() returned unexpected writer")
	}

	ctx.Header("X-Trace", "trace-2026")
	ctx.JSON(http.StatusCreated, map[string]string{"status": "saved"})

	if jsonRecorder.Code != http.StatusCreated {
		t.Errorf("response status = %d, want %d", jsonRecorder.Code, http.StatusCreated)
	}
	if got := jsonRecorder.Header().Get("X-Trace"); got != "trace-2026" {
		t.Errorf("response header X-Trace = %q, want %q", got, "trace-2026")
	}
	if !strings.Contains(jsonRecorder.Body.String(), `"status":"saved"`) {
		t.Errorf("response body = %q, want JSON payload", jsonRecorder.Body.String())
	}
}
