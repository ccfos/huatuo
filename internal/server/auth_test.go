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
)

func TestAuthServiceAuthenticate(t *testing.T) {
	svc := newTestAuthService()

	user, ok := svc.Authenticate("viewer-secret")
	if !ok {
		t.Fatal("Authenticate() did not find configured bearer token")
	}
	if user.ID != "viewer-2026" {
		t.Errorf("user ID = %q, want %q", user.ID, "viewer-2026")
	}
	if user.IsAdmin {
		t.Error("user IsAdmin = true, want false")
	}
	if len(user.Permissions) != 2 {
		t.Errorf("user permissions length = %d, want 2", len(user.Permissions))
	}

	if _, ok := svc.Authenticate("viewer-2026"); ok {
		t.Error("Authenticate() accepted the stable user ID as a bearer token")
	}
}

func TestAuthServiceValidate(t *testing.T) {
	svc := newTestAuthService()
	admin, _ := svc.Authenticate("admin-secret")
	viewer, _ := svc.Authenticate("viewer-secret")

	tests := []struct {
		name    string
		user    User
		method  string
		path    string
		wantErr bool
	}{
		{
			name:   "admin can access any path",
			user:   admin,
			method: http.MethodDelete,
			path:   "/v1/settings/runtime",
		},
		{
			name:   "method and wildcard match",
			user:   viewer,
			method: http.MethodGet,
			path:   "/v1/profiles/job-2026",
		},
		{
			name:    "method mismatch",
			user:    viewer,
			method:  http.MethodDelete,
			path:    "/v1/profiles/job-2026",
			wantErr: true,
		},
		{
			name:   "path parameter match",
			user:   viewer,
			method: http.MethodGet,
			path:   "/v1/tasks/task-2026",
		},
		{
			name:    "path mismatch",
			user:    viewer,
			method:  http.MethodGet,
			path:    "/v1/settings/runtime",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.Validate(tt.user, tt.method, tt.path)
			if tt.wantErr && err == nil {
				t.Fatal("Validate() error = nil, want permission denied")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestAuthServiceMatchesPath(t *testing.T) {
	svc := NewAuthService(nil)
	tests := []struct {
		name       string
		permission string
		path       string
		want       bool
	}{
		{name: "exact", permission: "/v1/tasks", path: "/v1/tasks", want: true},
		{
			name:       "double star",
			permission: "/v1/traces/**",
			path:       "/v1/traces/task-2026/detail",
			want:       true,
		},
		{
			name:       "path parameter",
			permission: "/v1/tasks/:taskID",
			path:       "/v1/tasks/task-2026",
			want:       true,
		},
		{
			name:       "single star",
			permission: "/v1/tasks/*/result",
			path:       "/v1/tasks/task-2026/result",
			want:       true,
		},
		{
			name:       "segment count mismatch",
			permission: "/v1/tasks",
			path:       "/v1/tasks/task-2026",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := svc.matchesPath(tt.permission, tt.path); got != tt.want {
				t.Errorf("matchesPath(%q, %q) = %v, want %v", tt.permission, tt.path, got, tt.want)
			}
		})
	}
}

func TestNewAuthMiddleware(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)
	svc := newTestAuthService()

	tests := []struct {
		name           string
		authHeader     string
		path           string
		wantStatus     int
		wantBodyPart   string
		wantHandlerRun bool
		wantUserID     string
		wantIsAdmin    bool
	}{
		{
			name:         "missing bearer token",
			path:         "/v1/tasks/task-2026",
			wantStatus:   http.StatusUnauthorized,
			wantBodyPart: "missing bearer token",
		},
		{
			name:         "stable ID is not a token",
			authHeader:   "Bearer viewer-2026",
			path:         "/v1/tasks/task-2026",
			wantStatus:   http.StatusUnauthorized,
			wantBodyPart: "invalid bearer token",
		},
		{
			name:         "permission denied",
			authHeader:   "Bearer viewer-secret",
			path:         "/v1/tasks/task-2026/result",
			wantStatus:   http.StatusForbidden,
			wantBodyPart: "does not have permission",
		},
		{
			name:           "authenticated stable principal",
			authHeader:     "Bearer viewer-secret",
			path:           "/v1/tasks/task-2026",
			wantStatus:     http.StatusNoContent,
			wantHandlerRun: true,
			wantUserID:     "viewer-2026",
		},
		{
			name:           "authenticated admin",
			authHeader:     "Bearer admin-secret",
			path:           "/v1/tasks/task-2026/result",
			wantStatus:     http.StatusNoContent,
			wantHandlerRun: true,
			wantUserID:     "admin-2026",
			wantIsAdmin:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := httpGin.New()
			var handlerRan bool
			var gotUserID string
			var gotIsAdmin bool
			handler := wrapHandler(func(ctx *Context) {
				handlerRan = true
				gotUserID = ctx.UserID
				gotIsAdmin = ctx.IsAdmin
				ctx.Status(http.StatusNoContent)
			})
			engine.GET(
				"/v1/tasks/:taskID",
				wrapHandler(NewAuthMiddleware(svc)),
				handler,
			)
			engine.GET(
				"/v1/tasks/:taskID/result",
				wrapHandler(NewAuthMiddleware(svc)),
				handler,
			)

			request := httptest.NewRequest(http.MethodGet, tt.path, http.NoBody)
			if tt.authHeader != "" {
				request.Header.Set("Authorization", tt.authHeader)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if recorder.Code != tt.wantStatus {
				t.Errorf("response status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if tt.wantBodyPart != "" &&
				!strings.Contains(recorder.Body.String(), tt.wantBodyPart) {
				t.Errorf(
					"response body = %q, want substring %q",
					recorder.Body.String(),
					tt.wantBodyPart,
				)
			}
			if handlerRan != tt.wantHandlerRun {
				t.Errorf("handler executed = %v, want %v", handlerRan, tt.wantHandlerRun)
			}
			if gotUserID != tt.wantUserID {
				t.Errorf("ctx.UserID = %q, want %q", gotUserID, tt.wantUserID)
			}
			if gotIsAdmin != tt.wantIsAdmin {
				t.Errorf("ctx.IsAdmin = %v, want %v", gotIsAdmin, tt.wantIsAdmin)
			}
		})
	}
}

func newTestAuthService() *authService {
	return NewAuthService([]UserConfig{
		{
			ID:          "admin-2026",
			BearerToken: "admin-secret",
			IsAdmin:     true,
		},
		{
			ID:          "viewer-2026",
			BearerToken: "viewer-secret",
			Permissions: []string{
				"GET /v1/profiles/**",
				"/v1/tasks/:taskID",
			},
		},
	})
}
