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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/ccfos/huatuo/apis/v1"
	"github.com/ccfos/huatuo/internal/server/response"
	"github.com/ccfos/huatuo/internal/version"

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

func TestNewServerUsesHTTPGuardDefaults(t *testing.T) {
	s := NewServer(nil)

	if s.config.ReadHeaderTimeout != defaultReadHeaderTimeout {
		t.Errorf(
			"ReadHeaderTimeout = %s, want %s",
			s.config.ReadHeaderTimeout,
			defaultReadHeaderTimeout,
		)
	}
	if s.config.ReadTimeout != defaultReadTimeout {
		t.Errorf("ReadTimeout = %s, want %s", s.config.ReadTimeout, defaultReadTimeout)
	}
	if s.config.WriteTimeout != defaultWriteTimeout {
		t.Errorf("WriteTimeout = %s, want %s", s.config.WriteTimeout, defaultWriteTimeout)
	}
	if s.config.IdleTimeout != defaultIdleTimeout {
		t.Errorf("IdleTimeout = %s, want %s", s.config.IdleTimeout, defaultIdleTimeout)
	}
	if s.config.MaxHeaderBytes != defaultMaxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d, want %d", s.config.MaxHeaderBytes, defaultMaxHeaderBytes)
	}
	if s.config.MaxBodyBytes != defaultMaxBodyBytes {
		t.Errorf("MaxBodyBytes = %d, want %d", s.config.MaxBodyBytes, defaultMaxBodyBytes)
	}
}

func TestNewServerDoesNotModifyConfig(t *testing.T) {
	cfg := Config{ReadTimeout: time.Second}

	s := NewServer(&cfg)

	if cfg.ReadHeaderTimeout != 0 {
		t.Errorf("input ReadHeaderTimeout = %s, want zero", cfg.ReadHeaderTimeout)
	}
	if cfg.ReadTimeout != time.Second {
		t.Errorf("input ReadTimeout = %s, want %s", cfg.ReadTimeout, time.Second)
	}
	if s.config.ReadHeaderTimeout != defaultReadHeaderTimeout {
		t.Errorf(
			"effective ReadHeaderTimeout = %s, want %s",
			s.config.ReadHeaderTimeout,
			defaultReadHeaderTimeout,
		)
	}
	if s.config.ReadTimeout != time.Second {
		t.Errorf("effective ReadTimeout = %s, want %s", s.config.ReadTimeout, time.Second)
	}
}

func TestNewServerUsesConfiguredErrorStatusMapper(t *testing.T) {
	const customCode v1.ErrorCode = "custom_conflict"
	mapper := func(code v1.ErrorCode) (int, bool) {
		return http.StatusConflict, code == customCode
	}
	s := NewServer(&Config{ErrorStatusMapper: mapper})
	s.MustRegisterRoutes("", []Route{{
		Method: http.MethodGet,
		Path:   "/custom-error",
		Handler: func(*Context) error {
			return response.NewAPIError(customCode, "custom conflict")
		},
	}})

	request := httptest.NewRequest(http.MethodGet, "/custom-error", http.NoBody)
	recorder := httptest.NewRecorder()
	s.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	if !strings.Contains(
		recorder.Body.String(),
		`"error":{"code":"custom_conflict","message":"custom conflict"}`,
	) {
		t.Errorf("response body = %q, want custom error", recorder.Body.String())
	}
}

func TestNewServerDoesNotRegisterProbeRoutes(t *testing.T) {
	s := NewServer(nil)

	for _, path := range []string{"/healthz", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		recorder := httptest.NewRecorder()
		s.engine.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want %d", path, recorder.Code, http.StatusNotFound)
		}
	}
}

func TestNewServerWritesRoutingErrors(t *testing.T) {
	s := NewServer(nil)
	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "route not found",
			method:     http.MethodGet,
			target:     "/missing",
			wantStatus: http.StatusNotFound,
			wantCode:   "route_not_found",
		},
		{
			name:       "method not allowed",
			method:     http.MethodPost,
			target:     "/metrics",
			wantStatus: http.StatusMethodNotAllowed,
			wantCode:   "method_not_allowed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.target, http.NoBody)
			recorder := httptest.NewRecorder()
			s.engine.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Errorf("response status = %d, want %d", recorder.Code, test.wantStatus)
			}
			if !strings.Contains(recorder.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Errorf("response body = %q, want code %q", recorder.Body.String(), test.wantCode)
			}
			if test.wantStatus == http.StatusMethodNotAllowed {
				if got := recorder.Header().Get("Allow"); got != http.MethodGet {
					t.Errorf("Allow header = %q, want %q", got, http.MethodGet)
				}
			}
		})
	}
}

func TestNewServerRecoversPanicAsJSONError(t *testing.T) {
	s := NewServer(nil)
	s.MustRegisterRoutes("", []Route{{
		Method: http.MethodGet,
		Path:   "/panic",
		Handler: func(*Context) error {
			panic("private panic detail")
		},
	}})

	request := httptest.NewRequest(http.MethodGet, "/panic", http.NoBody)
	recorder := httptest.NewRecorder()
	s.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(
		recorder.Body.String(),
		`"error":{"code":"internal_error","message":"internal error"}`,
	) {
		t.Errorf("response body = %q, want internal error", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "private panic detail") {
		t.Errorf("response body = %q, exposed panic detail", recorder.Body.String())
	}
}

func TestNewServerRegistersVersionRoute(t *testing.T) {
	info := version.Info{
		Name:         "huatuo-apiserver",
		Version:      "1.2.3",
		GitCommit:    "abcdef123456",
		GitTreeState: "clean",
		BuildTime:    "2026-06-24T00:00:00Z",
		GoVersion:    "go1.24.0",
		Compiler:     "gc",
		Platform:     "linux/amd64",
	}
	s := NewServer(&Config{VersionInfo: &info})

	request := httptest.NewRequest(http.MethodGet, "/version", http.NoBody)
	recorder := httptest.NewRecorder()

	s.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var got struct {
		Data version.Info `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, recorder.Body.String())
	}
	if got.Data != info {
		t.Errorf("version response = %+v, want %+v", got.Data, info)
	}
}

func TestServerAuthPolicyKeepsMetricsPublicAndPProfAdminOnly(t *testing.T) {
	srv := NewServer(&Config{
		EnablePProf: true,
		AdminPaths:  []string{"/v1/profiles/flamegraph/**"},
		AuthUsers: []UserConfig{
			{ID: "admin-2026", BearerToken: "admin-secret", IsAdmin: true},
			{ID: "viewer-2026", BearerToken: "viewer-secret", Permissions: []string{
				"/debug/pprof/**",
				"/v1/profiles/flamegraph/**",
			}},
		},
	})
	srv.engine.POST("/v1/profiles/flamegraph/query", func(ctx *httpGin.Context) {
		ctx.Status(http.StatusNoContent)
	})

	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
	metricsRecorder := httptest.NewRecorder()
	srv.engine.ServeHTTP(metricsRecorder, metricsRequest)
	if metricsRecorder.Code != http.StatusNotImplemented {
		t.Fatalf("anonymous metrics status=%d, want %d", metricsRecorder.Code, http.StatusNotImplemented)
	}

	viewerRequest := httptest.NewRequest(http.MethodGet, "/debug/pprof/", http.NoBody)
	viewerRequest.Header.Set("Authorization", "Bearer viewer-secret")
	viewerRecorder := httptest.NewRecorder()
	srv.engine.ServeHTTP(viewerRecorder, viewerRequest)
	if viewerRecorder.Code != http.StatusForbidden {
		t.Fatalf("viewer pprof status=%d, want %d", viewerRecorder.Code, http.StatusForbidden)
	}

	adminRequest := httptest.NewRequest(http.MethodGet, "/debug/pprof/", http.NoBody)
	adminRequest.Header.Set("Authorization", "Bearer admin-secret")
	adminRecorder := httptest.NewRecorder()
	srv.engine.ServeHTTP(adminRecorder, adminRequest)
	if adminRecorder.Code != http.StatusOK {
		t.Fatalf("admin pprof status=%d, want %d", adminRecorder.Code, http.StatusOK)
	}

	viewerFlameRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/profiles/flamegraph/query",
		http.NoBody,
	)
	viewerFlameRequest.Header.Set("Authorization", "Bearer viewer-secret")
	viewerFlameRecorder := httptest.NewRecorder()
	srv.engine.ServeHTTP(viewerFlameRecorder, viewerFlameRequest)
	if viewerFlameRecorder.Code != http.StatusForbidden {
		t.Fatalf("viewer flamegraph status=%d, want %d", viewerFlameRecorder.Code, http.StatusForbidden)
	}
}

func TestPromServerHandlerWithRegistry(t *testing.T) {
	s := &Server{promRegistry: prometheus.NewRegistry()}

	handler := s.metricsHandler()
	ctx, recorder := newTestServerContext(http.MethodGet, "/metrics", "")

	err := handler(ctx)
	if err != nil {
		t.Errorf("metricsHandler() error = %v", err)
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
	if !strings.Contains(
		secondRecorder.Body.String(),
		`"error":{"code":"rate_limited","message":"too many requests"}`,
	) {
		t.Errorf("second response body = %q, want rate limit error", secondRecorder.Body.String())
	}
}

func TestNewServerRateLimit(t *testing.T) {
	tests := []struct {
		name                 string
		rateLimit            *RateLimitConfig
		expectedSecondStatus int
	}{
		{
			name:                 "disabled by default",
			expectedSecondStatus: http.StatusNoContent,
		},
		{
			name:                 "zero config disables rate limiting",
			rateLimit:            &RateLimitConfig{},
			expectedSecondStatus: http.StatusNoContent,
		},
		{
			name: "enabled when configured",
			rateLimit: &RateLimitConfig{
				RequestsPerSecond: 1,
				Burst:             1,
			},
			expectedSecondStatus: http.StatusTooManyRequests,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := NewServer(&Config{RateLimit: test.rateLimit})
			s.MustRegisterRoutes("", []Route{{
				Method: http.MethodGet,
				Path:   "/tasks",
				Handler: func(ctx *Context) error {
					ctx.Status(http.StatusNoContent)
					return nil
				},
			}})

			firstRequest := httptest.NewRequest(http.MethodGet, "/tasks", http.NoBody)
			firstRecorder := httptest.NewRecorder()
			s.engine.ServeHTTP(firstRecorder, firstRequest)
			if firstRecorder.Code != http.StatusNoContent {
				t.Fatalf("first response status = %d, want %d", firstRecorder.Code, http.StatusNoContent)
			}

			secondRequest := httptest.NewRequest(http.MethodGet, "/tasks", http.NoBody)
			secondRecorder := httptest.NewRecorder()
			s.engine.ServeHTTP(secondRecorder, secondRequest)
			if secondRecorder.Code != test.expectedSecondStatus {
				t.Errorf(
					"second response status = %d, want %d",
					secondRecorder.Code,
					test.expectedSecondStatus,
				)
			}
		})
	}
}

func TestServerMustRegisterRoutes(t *testing.T) {
	s := NewServer(nil)
	s.MustRegisterRoutes("/tasks", []Route{
		{
			Method: MethodAny,
			Path:   "/disabled",
			Handler: func(ctx *Context) error {
				ctx.JSON(http.StatusServiceUnavailable, map[string]string{"method": ctx.Request().Method})
				return nil
			},
		},
		{
			Method: http.MethodGet,
			Path:   "/status",
			Handler: func(ctx *Context) error {
				ctx.JSON(http.StatusOK, map[string]string{"method": http.MethodGet})
				return nil
			},
		},
		{
			Method: http.MethodPost,
			Path:   "",
			Handler: func(ctx *Context) error {
				ctx.JSON(http.StatusCreated, map[string]string{"method": http.MethodPost})
				return nil
			},
		},
		{
			Method: http.MethodDelete,
			Path:   "/task-20250226",
			Handler: func(ctx *Context) error {
				ctx.Status(http.StatusNoContent)
				return nil
			},
		},
		{
			Method: http.MethodPut,
			Path:   "/task-20250226",
			Handler: func(ctx *Context) error {
				ctx.JSON(http.StatusAccepted, map[string]string{"method": http.MethodPut})
				return nil
			},
		},
		{
			Method: http.MethodPatch,
			Path:   "/task-20250226",
			Handler: func(ctx *Context) error {
				ctx.JSON(http.StatusOK, map[string]string{"method": http.MethodPatch})
				return nil
			},
		},
		{
			Method: "PROPFIND",
			Path:   "/extended",
			Handler: func(ctx *Context) error {
				ctx.Status(http.StatusNoContent)
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
			name:         "any-route",
			method:       http.MethodOptions,
			target:       "/tasks/disabled",
			wantStatus:   http.StatusServiceUnavailable,
			wantBodyPart: `"method":"OPTIONS"`,
		},
		{
			name:       "extension-method-route",
			method:     "PROPFIND",
			target:     "/tasks/extended",
			wantStatus: http.StatusNoContent,
		},
		{
			name:         "get-route",
			method:       http.MethodGet,
			target:       "/tasks/status",
			wantStatus:   http.StatusOK,
			wantBodyPart: `"method":"GET"`,
		},
		{
			name:         "post-route",
			method:       http.MethodPost,
			target:       "/tasks",
			wantStatus:   http.StatusCreated,
			wantBodyPart: `"method":"POST"`,
		},
		{
			name:       "delete-route",
			method:     http.MethodDelete,
			target:     "/tasks/task-20250226",
			wantStatus: http.StatusNoContent,
		},
		{
			name:         "put-route",
			method:       http.MethodPut,
			target:       "/tasks/task-20250226",
			wantStatus:   http.StatusAccepted,
			wantBodyPart: `"method":"PUT"`,
		},
		{
			name:         "patch-route",
			method:       http.MethodPatch,
			target:       "/tasks/task-20250226",
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

func TestServerMustRegisterRoutesPanicsWithoutMethod(t *testing.T) {
	s := NewServer(nil)
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Errorf("MustRegisterRoutes() did not panic for missing HTTP method")
			return
		}
		if recovered != `route "/tasks" has no http method` {
			t.Errorf(
				"panic value = %v, want %q",
				recovered,
				`route "/tasks" has no http method`,
			)
		}
	}()

	s.MustRegisterRoutes("", []Route{
		{Path: "/tasks"},
	})
}
