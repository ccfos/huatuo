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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	serverapi "huatuo-bamai/apis/v1/server"
	"huatuo-bamai/internal/server/response"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers/legacy"
	httpGin "github.com/gin-gonic/gin"
)

func TestBundledOpenAPIRouterMatchesMethod(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(serverapi.OpenAPIJSON())
	if err != nil {
		t.Fatalf("load specification: %v", err)
	}
	router, err := legacy.NewRouter(document)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/profiling", http.NoBody)
	route, _, err := router.FindRoute(request)
	if err != nil {
		t.Fatalf("FindRoute() error = %v", err)
	}
	if route.Operation.OperationID != "createProfilingJob" {
		t.Errorf("operation ID = %q, want createProfilingJob", route.Operation.OperationID)
	}
	if route.Operation.RequestBody == nil {
		t.Error("createProfilingJob has no request body")
	}
}

func TestRegisterOpenAPIHandlersValidatesRequests(t *testing.T) {
	s := NewServer(&Config{
		ErrorStatusMapper: serverapi.HTTPStatusForErrorCode,
		MaxBodyBytes:      256,
	})
	var calls int
	err := s.RegisterOpenAPIHandlers(serverapi.OpenAPIJSON(), func(router httpGin.IRouter) {
		router.POST("/v1/profiling", func(ctx *httpGin.Context) {
			calls++
			ctx.Status(http.StatusNoContent)
		})
	})
	if err != nil {
		t.Fatalf("RegisterOpenAPIHandlers() error = %v", err)
	}

	validBody := `{"hostname":"node-a","duration_seconds":10,"scope":"host",` +
		`"type":"cpu","language":"go","mode":"oncpu"}`
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int
		wantCode    string
		wantCalls   int
	}{
		{
			name:        "valid",
			contentType: "application/json",
			body:        validBody,
			wantStatus:  http.StatusNoContent,
			wantCalls:   1,
		},
		{
			name:        "schema violation",
			contentType: "application/json",
			body:        `{}`,
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_request",
			wantCalls:   1,
		},
		{
			name:        "missing required body",
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_request",
			wantCalls:   1,
		},
		{
			name:        "unsupported content type",
			contentType: "text/plain",
			body:        validBody,
			wantStatus:  http.StatusUnsupportedMediaType,
			wantCode:    "unsupported_media_type",
			wantCalls:   1,
		},
		{
			name:        "body too large",
			contentType: "application/json",
			body:        `{"padding":"` + strings.Repeat("x", 512) + `"}`,
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    "request_too_large",
			wantCalls:   1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/profiling",
				strings.NewReader(test.body),
			)
			request.Header.Set("Content-Type", test.contentType)
			recorder := httptest.NewRecorder()

			s.engine.ServeHTTP(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Errorf(
					"response status = %d, want %d; body=%s",
					recorder.Code,
					test.wantStatus,
					recorder.Body.String(),
				)
			}
			if test.wantCode != "" && !strings.Contains(
				recorder.Body.String(),
				`"code":"`+test.wantCode+`"`,
			) {
				t.Errorf("response body = %q, want code %q", recorder.Body.String(), test.wantCode)
			}
			if calls != test.wantCalls {
				t.Errorf("handler calls = %d, want %d", calls, test.wantCalls)
			}
		})
	}
}

func TestRegisterOpenAPIHandlersRejectsInvalidSetup(t *testing.T) {
	s := NewServer(nil)

	if err := s.RegisterOpenAPIHandlers(serverapi.OpenAPIJSON(), nil); err == nil {
		t.Fatal("RegisterOpenAPIHandlers() error = nil, want missing register function error")
	}
	if err := s.RegisterOpenAPIHandlers([]byte("{"), func(httpGin.IRouter) {}); err == nil {
		t.Fatal("RegisterOpenAPIHandlers() error = nil, want invalid specification error")
	}
}

func TestOpenAPIValidatorRejectsUndeclaredBody(t *testing.T) {
	s := NewServer(&Config{ErrorStatusMapper: serverapi.HTTPStatusForErrorCode})
	var called bool
	err := s.RegisterOpenAPIHandlers(serverapi.OpenAPIJSON(), func(router httpGin.IRouter) {
		router.GET("/v1/profiling/capabilities", func(ctx *httpGin.Context) {
			called = true
			ctx.Status(http.StatusNoContent)
		})
	})
	if err != nil {
		t.Fatalf("RegisterOpenAPIHandlers() error = %v", err)
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/profiling/capabilities",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), `"code":"invalid_request"`) {
		t.Errorf("response body = %q, want invalid request", recorder.Body.String())
	}
	if called {
		t.Error("handler was called for a request with an undeclared body")
	}
}

func TestStrictErrorHandlers(t *testing.T) {
	handlers := NewStrictErrorHandlers(serverapi.HTTPStatusForErrorCode)
	tests := []struct {
		name        string
		invoke      func(*httpGin.Context)
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{
			name: "invalid request",
			invoke: func(ctx *httpGin.Context) {
				handlers.RequestError(ctx, errors.New("malformed JSON"))
			},
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_request",
			wantMessage: "invalid request",
		},
		{
			name: "request too large",
			invoke: func(ctx *httpGin.Context) {
				handlers.RequestError(ctx, &http.MaxBytesError{Limit: 10})
			},
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    "request_too_large",
			wantMessage: "request body is too large",
		},
		{
			name: "business error",
			invoke: func(ctx *httpGin.Context) {
				handlers.HandlerError(ctx, response.NewAPIError(
					serverapi.ErrorCodeJobConflict,
					"job cannot be stopped",
				))
			},
			wantStatus:  http.StatusConflict,
			wantCode:    "job_conflict",
			wantMessage: "job cannot be stopped",
		},
		{
			name: "unknown handler error",
			invoke: func(ctx *httpGin.Context) {
				handlers.HandlerError(ctx, errors.New("storage password leaked"))
			},
			wantStatus:  http.StatusInternalServerError,
			wantCode:    "internal_error",
			wantMessage: "internal error",
		},
		{
			name: "response serialization error",
			invoke: func(ctx *httpGin.Context) {
				handlers.ResponseError(ctx, errors.New("encode response"))
			},
			wantStatus:  http.StatusInternalServerError,
			wantCode:    "internal_error",
			wantMessage: "internal error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := httpGin.CreateTestContext(recorder)
			test.invoke(ctx)

			if recorder.Code != test.wantStatus {
				t.Errorf("response status = %d, want %d", recorder.Code, test.wantStatus)
			}
			wantBody := `"error":{"code":"` + test.wantCode +
				`","message":"` + test.wantMessage + `"}`
			if !strings.Contains(recorder.Body.String(), wantBody) {
				t.Errorf("response body = %q, want %q", recorder.Body.String(), wantBody)
			}
		})
	}
}

func TestGeneratedStrictHandlerUsesSharedErrorBoundary(t *testing.T) {
	s := NewServer(&Config{ErrorStatusMapper: serverapi.HTTPStatusForErrorCode})
	handler := &strictServerTestHandler{}
	errorHandlers := s.StrictErrorHandlers()
	strictHandler := serverapi.NewStrictHandlerWithOptions(
		handler,
		nil,
		serverapi.StrictGinServerOptions{
			RequestErrorHandlerFunc:  errorHandlers.RequestError,
			HandlerErrorFunc:         errorHandlers.HandlerError,
			ResponseErrorHandlerFunc: errorHandlers.ResponseError,
		},
	)
	if err := s.RegisterOpenAPIHandlers(serverapi.OpenAPIJSON(), func(router httpGin.IRouter) {
		serverapi.RegisterHandlers(router, strictHandler)
	}); err != nil {
		t.Fatalf("RegisterOpenAPIHandlers() error = %v", err)
	}

	body := `{"hostname":"node-a","duration_seconds":10,"scope":"host",` +
		`"type":"cpu","language":"go","mode":"oncpu"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/profiling", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Errorf("response status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	if !strings.Contains(
		recorder.Body.String(),
		`"error":{"code":"job_conflict","message":"job already exists"}`,
	) {
		t.Errorf("response body = %q, want job conflict", recorder.Body.String())
	}
	if handler.calls != 1 {
		t.Errorf("CreateProfilingJob() calls = %d, want 1", handler.calls)
	}
}

type strictServerTestHandler struct {
	serverapi.StrictServerInterface
	calls int
}

var _ serverapi.StrictServerInterface = (*strictServerTestHandler)(nil)

func (h *strictServerTestHandler) CreateProfilingJob(
	context.Context,
	serverapi.CreateProfilingJobRequestObject,
) (serverapi.CreateProfilingJobResponseObject, error) {
	h.calls++
	return nil, response.NewAPIError(serverapi.ErrorCodeJobConflict, "job already exists")
}
