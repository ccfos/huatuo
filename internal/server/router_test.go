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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpGin "github.com/gin-gonic/gin"
)

type stubAPIError struct {
	httpStatus int
	code       int
	message    string
}

func (e stubAPIError) Error() string {
	return e.message
}

func (e stubAPIError) GetHTTPStatus() int {
	return e.httpStatus
}

func (e stubAPIError) GetCode() int {
	return e.code
}

func (e stubAPIError) GetMessage() string {
	return e.message
}

func TestNewRootAndRouterGroupMethods(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	engine := httpGin.New()
	root := NewRoot(engine, "/api")
	root.Use(func(ctx *Context) {
		ctx.Header("X-Region", "huatuo-region")
		ctx.Next()
	})

	root.GET("/status", func(ctx *Context) error {
		ctx.JSON(http.StatusOK, map[string]string{"method": http.MethodGet})
		return nil
	})
	root.POST("/tasks", func(ctx *Context) error {
		ctx.JSON(http.StatusCreated, map[string]string{"method": http.MethodPost})
		return nil
	})
	root.DELETE("/tasks/:taskID", func(ctx *Context) error {
		ctx.Status(http.StatusNoContent)
		return nil
	})
	root.PUT("/tasks/:taskID", func(ctx *Context) error {
		ctx.JSON(http.StatusAccepted, map[string]string{"method": http.MethodPut})
		return nil
	})
	root.Handle(http.MethodPatch, "/tasks/:taskID/retry", func(ctx *Context) error {
		ctx.JSON(http.StatusAccepted, map[string]string{"method": http.MethodPatch})
		return nil
	})

	cases := []struct {
		name           string
		method         string
		target         string
		wantStatus     int
		wantBodyPart   string
		wantHeaderPart string
	}{
		{
			name:           "get-route",
			method:         http.MethodGet,
			target:         "/api/status",
			wantStatus:     http.StatusOK,
			wantBodyPart:   `"method":"GET"`,
			wantHeaderPart: "huatuo-region",
		},
		{
			name:           "post-route",
			method:         http.MethodPost,
			target:         "/api/tasks",
			wantStatus:     http.StatusCreated,
			wantBodyPart:   `"method":"POST"`,
			wantHeaderPart: "huatuo-region",
		},
		{
			name:           "delete-route",
			method:         http.MethodDelete,
			target:         "/api/tasks/task-20250226",
			wantStatus:     http.StatusNoContent,
			wantHeaderPart: "huatuo-region",
		},
		{
			name:           "put-route",
			method:         http.MethodPut,
			target:         "/api/tasks/task-20250226",
			wantStatus:     http.StatusAccepted,
			wantBodyPart:   `"method":"PUT"`,
			wantHeaderPart: "huatuo-region",
		},
		{
			name:           "handle-route",
			method:         http.MethodPatch,
			target:         "/api/tasks/task-20250226/retry",
			wantStatus:     http.StatusAccepted,
			wantBodyPart:   `"method":"PATCH"`,
			wantHeaderPart: "huatuo-region",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.target, http.NoBody)
			recorder := httptest.NewRecorder()

			engine.ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Errorf("response status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if tc.wantBodyPart != "" && !strings.Contains(recorder.Body.String(), tc.wantBodyPart) {
				t.Errorf("response body = %q, want substring %q", recorder.Body.String(), tc.wantBodyPart)
			}
			if got := recorder.Header().Get("X-Region"); got != tc.wantHeaderPart {
				t.Errorf("response header X-Region = %q, want %q", got, tc.wantHeaderPart)
			}
		})
	}
}

func TestRouterGroupGroupCreatesNestedRoutes(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	engine := httpGin.New()
	group := NewRoot(engine, "/api").Group("/v1")
	group.GET("/tasks/:taskID", func(ctx *Context) error {
		ctx.JSON(http.StatusOK, map[string]string{"taskID": ctx.Param("taskID")})
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/task-20250226", http.NoBody)
	recorder := httptest.NewRecorder()

	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), `"taskID":"task-20250226"`) {
		t.Errorf("response body = %q, want task identifier", recorder.Body.String())
	}
}

func TestWrapHandlerUsesInternalContext(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := httpGin.CreateTestContext(recorder)

	want := newContext(ginCtx)
	var called bool

	wrapped := wrapHandler(func(ctx *Context) {
		called = true
		if ctx != want {
			t.Errorf("handler context = %p, want %p", ctx, want)
		}
	})
	wrapped(ginCtx)

	if !called {
		t.Errorf("wrapped handler was not called")
	}
}

func TestWrapErrHandlerWritesErrors(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantStatus   int
		wantBodyPart string
	}{
		{
			name:         "api-style-error",
			err:          stubAPIError{httpStatus: http.StatusBadRequest, code: 4001, message: "invalid payload"},
			wantStatus:   http.StatusBadRequest,
			wantBodyPart: `"message":"invalid payload"`,
		},
		{
			name:         "generic-error",
			err:          errors.New("trace-2026 failed"),
			wantStatus:   http.StatusInternalServerError,
			wantBodyPart: `"message":"trace-2026 failed"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginCtx, _ := httpGin.CreateTestContext(recorder)
			ginCtx.Request = httptest.NewRequest(http.MethodGet, "/api/status", http.NoBody)

			wrapped := wrapErrHandler(func(*Context) error {
				return tc.err
			})
			wrapped(ginCtx)

			if recorder.Code != tc.wantStatus {
				t.Errorf("response status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if !strings.Contains(recorder.Body.String(), tc.wantBodyPart) {
				t.Errorf("response body = %q, want substring %q", recorder.Body.String(), tc.wantBodyPart)
			}
		})
	}
}

func TestWriteErrorFallback(t *testing.T) {
	ctx, recorder := newTestServerContext(http.MethodGet, "/api/status", "")

	writeError(ctx, errors.New("unexpected failure"))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(recorder.Body.String(), `"code":500`) {
		t.Errorf("response body = %q, want error code 500", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"message":"unexpected failure"`) {
		t.Errorf("response body = %q, want failure message", recorder.Body.String())
	}
}
