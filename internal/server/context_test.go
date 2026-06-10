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

func TestNewContextAndInternalContext(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := httpGin.CreateTestContext(recorder)

	want := newContext(ginCtx)
	got := internalContext(ginCtx)
	if got != want {
		t.Errorf("internalContext() returned %p, want %p", got, want)
	}

	storedValue, ok := ginCtx.Get(contextKey)
	if !ok {
		t.Errorf("gin context missing %q after newContext", contextKey)
		return
	}

	storedCtx, ok := storedValue.(*Context)
	if !ok {
		t.Errorf("stored context type = %T, want *Context", storedValue)
		return
	}

	if storedCtx != want {
		t.Errorf("stored context = %p, want %p", storedCtx, want)
	}
}

func TestInternalContextCreatesFallbackWhenMissingOrInvalid(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*httpGin.Context)
	}{
		{
			name:  "missing-context",
			setup: func(*httpGin.Context) {},
		},
		{
			name: "invalid-context-type",
			setup: func(c *httpGin.Context) {
				c.Set(contextKey, "trace-2026")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginCtx, _ := httpGin.CreateTestContext(recorder)
			tc.setup(ginCtx)

			got := internalContext(ginCtx)
			if got == nil {
				t.Errorf("internalContext() returned nil")
				return
			}
			if got.c != ginCtx {
				t.Errorf("context wrapped gin.Context %p, want %p", got.c, ginCtx)
			}

			storedValue, ok := ginCtx.Get(contextKey)
			if !ok {
				t.Errorf("gin context missing %q after fallback creation", contextKey)
				return
			}

			storedCtx, ok := storedValue.(*Context)
			if !ok {
				t.Errorf("stored context type = %T, want *Context", storedValue)
				return
			}

			if storedCtx != got {
				t.Errorf("stored context = %p, want %p", storedCtx, got)
			}
		})
	}
}

func TestMiddlewareContextInjectsContext(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	engine := httpGin.New()
	engine.Use(middlewareContext())

	var hadStoredContext bool
	var paramValue string
	engine.GET("/tasks/:id", func(c *httpGin.Context) {
		_, hadStoredContext = c.Get(contextKey)
		paramValue = internalContext(c).Param("id")
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodGet, "/tasks/task-20250226", http.NoBody)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if !hadStoredContext {
		t.Errorf("middlewareContext() did not store request context")
	}
	if paramValue != "task-20250226" {
		t.Errorf("Param(id) = %q, want %q", paramValue, "task-20250226")
	}
}

func TestContextAccessorsAndBinding(t *testing.T) {
	ctx, recorder := newTestServerContext(
		http.MethodPost,
		"/tasks/task-20250226?view=full",
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

	if recorder.Code != http.StatusCreated {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if got := recorder.Header().Get("X-Trace"); got != "trace-2026" {
		t.Errorf("response header X-Trace = %q, want %q", got, "trace-2026")
	}
	if !strings.Contains(recorder.Body.String(), `"status":"saved"`) {
		t.Errorf("response body = %q, want JSON payload", recorder.Body.String())
	}
}

func TestContextShouldBindQueryAndBodyWith(t *testing.T) {
	ctx, _ := newTestServerContext(
		http.MethodPost,
		"/tasks?limit=5&view=full",
		`{"hostname":"huatuo-dev","region":"huatuo-region"}`,
	)

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

	var payload struct {
		Hostname string `json:"hostname"`
		Region   string `json:"region"`
	}
	if err := ctx.ShouldBindBodyWith(&payload, httpBinding.JSON); err != nil {
		t.Errorf("ShouldBindBodyWith() error = %v", err)
	}
	if payload.Hostname != "huatuo-dev" {
		t.Errorf("Hostname = %q, want %q", payload.Hostname, "huatuo-dev")
	}
	if payload.Region != "huatuo-region" {
		t.Errorf("Region = %q, want %q", payload.Region, "huatuo-region")
	}
}

func TestContextCanAccessTask(t *testing.T) {
	cases := []struct {
		name       string
		ctx        Context
		taskUserID string
		want       bool
	}{
		{
			name:       "admin-can-access-any-task",
			ctx:        Context{UserID: "viewer-2026", IsAdmin: true},
			taskUserID: "owner-2026",
			want:       true,
		},
		{
			name:       "owner-can-access-own-task",
			ctx:        Context{UserID: "owner-2026"},
			taskUserID: "owner-2026",
			want:       true,
		},
		{
			name:       "other-user-cannot-access-task",
			ctx:        Context{UserID: "viewer-2026"},
			taskUserID: "owner-2026",
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ctx.CanAccessTask(tc.taskUserID); got != tc.want {
				t.Errorf("CanAccessTask(%q) = %v, want %v", tc.taskUserID, got, tc.want)
			}
		})
	}
}
