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
	"time"

	httpGin "github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

func TestNewServerRegistersMetricsRouteWithoutRegistry(t *testing.T) {
	s := NewServer(nil)

	request := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	recorder := httptest.NewRecorder()

	s.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotImplemented {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusNotImplemented)
	}
	if !strings.Contains(recorder.Body.String(), "Prometheus registry not supported now") {
		t.Errorf("response body = %q, want metrics unsupported message", recorder.Body.String())
	}
}

func TestPromServerHandlerWithRegistry(t *testing.T) {
	s := &server{promRegistry: prometheus.NewRegistry()}

	handler := s.promServerHandler()
	ctx, recorder := newTestServerContext(http.MethodGet, "/metrics", "")

	err := handler(ctx)
	if err != nil {
		t.Errorf("promServerHandler() error = %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestNewRateLimitMiddleware(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	engine := httpGin.New()
	engine.Use(middlewareContext(), newRateLimitMiddleware(rate.Every(time.Hour), 1))
	engine.GET("/tasks", func(c *httpGin.Context) {
		c.Status(http.StatusOK)
	})

	firstRequest := httptest.NewRequest(http.MethodGet, "/tasks", http.NoBody)
	firstRecorder := httptest.NewRecorder()
	engine.ServeHTTP(firstRecorder, firstRequest)

	secondRequest := httptest.NewRequest(http.MethodGet, "/tasks", http.NoBody)
	secondRecorder := httptest.NewRecorder()
	engine.ServeHTTP(secondRecorder, secondRequest)

	if firstRecorder.Code != http.StatusOK {
		t.Errorf("first response status = %d, want %d", firstRecorder.Code, http.StatusOK)
	}
	if secondRecorder.Code != http.StatusTooManyRequests {
		t.Errorf("second response status = %d, want %d", secondRecorder.Code, http.StatusTooManyRequests)
	}
	if !strings.Contains(secondRecorder.Body.String(), `"message":"too many requests"`) {
		t.Errorf("second response body = %q, want rate limit message", secondRecorder.Body.String())
	}
}

func TestServerGroupReturnsConfiguredRootGroup(t *testing.T) {
	s := NewServer(&Config{Group: "/v1"})

	s.Group().GET("/status", func(ctx *Context) error {
		ctx.Status(http.StatusNoContent)
		return nil
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/status", http.NoBody)
	recorder := httptest.NewRecorder()

	s.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestServerMustRegisterRoutes(t *testing.T) {
	s := NewServer(&Config{Group: "/api"})
	s.MustRegisterRoutes("/tasks", []Handle{
		{
			Typ: HttpGet,
			Uri: "/status",
			Handle: func(ctx *Context) error {
				ctx.JSON(http.StatusOK, map[string]string{"method": http.MethodGet})
				return nil
			},
		},
		{
			Typ: HttpPost,
			Uri: "",
			Handle: func(ctx *Context) error {
				ctx.JSON(http.StatusCreated, map[string]string{"method": http.MethodPost})
				return nil
			},
		},
		{
			Typ: HttpDelete,
			Uri: "/task-20250226",
			Handle: func(ctx *Context) error {
				ctx.Status(http.StatusNoContent)
				return nil
			},
		},
		{
			Typ: HttpPut,
			Uri: "/task-20250226",
			Handle: func(ctx *Context) error {
				ctx.JSON(http.StatusAccepted, map[string]string{"method": http.MethodPut})
				return nil
			},
		},
		{
			Typ: HttpPatch,
			Uri: "/task-20250226",
			Handle: func(ctx *Context) error {
				ctx.JSON(http.StatusOK, map[string]string{"method": http.MethodPatch})
				return nil
			},
		},
	})

	cases := []struct {
		name         string
		method       string
		target       string
		wantStatus   int
		wantBodyPart string
	}{
		{
			name:         "get-route",
			method:       http.MethodGet,
			target:       "/api/tasks/status",
			wantStatus:   http.StatusOK,
			wantBodyPart: `"method":"GET"`,
		},
		{
			name:         "post-route",
			method:       http.MethodPost,
			target:       "/api/tasks",
			wantStatus:   http.StatusCreated,
			wantBodyPart: `"method":"POST"`,
		},
		{
			name:       "delete-route",
			method:     http.MethodDelete,
			target:     "/api/tasks/task-20250226",
			wantStatus: http.StatusNoContent,
		},
		{
			name:         "put-route",
			method:       http.MethodPut,
			target:       "/api/tasks/task-20250226",
			wantStatus:   http.StatusAccepted,
			wantBodyPart: `"method":"PUT"`,
		},
		{
			name:         "patch-route",
			method:       http.MethodPatch,
			target:       "/api/tasks/task-20250226",
			wantStatus:   http.StatusOK,
			wantBodyPart: `"method":"PATCH"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.target, http.NoBody)
			recorder := httptest.NewRecorder()

			s.engine.ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Errorf("response status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if tc.wantBodyPart != "" && !strings.Contains(recorder.Body.String(), tc.wantBodyPart) {
				t.Errorf("response body = %q, want substring %q", recorder.Body.String(), tc.wantBodyPart)
			}
		})
	}
}

func TestServerMustRegisterRoutesPanicsOnUnknownType(t *testing.T) {
	s := NewServer(nil)
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Errorf("MustRegisterRoutes() did not panic for unknown handler type")
			return
		}
		if recovered != "unknown type" {
			t.Errorf("panic value = %v, want %q", recovered, "unknown type")
		}
	}()

	s.MustRegisterRoutes("", []Handle{
		{Typ: 99, Uri: "/tasks"},
	})
}
