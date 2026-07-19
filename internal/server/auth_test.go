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

func TestNewAuthService(t *testing.T) {
	svc := NewAuthService([]UserConfig{
		{
			ID:          "admin-2026",
			Name:        "Admin User",
			Permissions: []string{"/v1/tasks/**"},
			IsAdmin:     true,
		},
		{
			ID:          "viewer-2026",
			Name:        "Viewer User",
			Permissions: []string{"/v1/tasks/:taskID", "/v1/tasks/*/result"},
		},
	})

	adminUser, ok := svc.GetUserById("admin-2026")
	if !ok {
		t.Errorf("GetUserById(admin-2026) did not find user")
		return
	}
	if !adminUser.IsAdmin {
		t.Errorf("admin user IsAdmin = false, want true")
	}
	if len(adminUser.Permissions) != 1 || adminUser.Permissions[0] != Permission("/v1/tasks/**") {
		t.Errorf("admin user permissions = %#v, want [/v1/tasks/**]", adminUser.Permissions)
	}

	viewerUser, ok := svc.GetUserById("viewer-2026")
	if !ok {
		t.Errorf("GetUserById(viewer-2026) did not find user")
		return
	}
	if viewerUser.Name != "Viewer User" {
		t.Errorf("viewer user name = %q, want %q", viewerUser.Name, "Viewer User")
	}
	if len(viewerUser.Permissions) != 2 {
		t.Errorf("viewer user permissions length = %d, want %d", len(viewerUser.Permissions), 2)
	}
}

func TestAuthServiceAddDeleteAndGetUserById(t *testing.T) {
	svc := NewAuthService(nil)
	user := User{
		ID:          "operator-2026",
		Name:        "Operator User",
		Permissions: []Permission{"/v1/config"},
	}

	svc.Add(user)

	got, ok := svc.GetUserById("operator-2026")
	if !ok {
		t.Errorf("GetUserById(operator-2026) did not find user after Add")
		return
	}
	if got.ID != user.ID {
		t.Errorf("user ID = %q, want %q", got.ID, user.ID)
	}
	if got.Name != user.Name {
		t.Errorf("user name = %q, want %q", got.Name, user.Name)
	}
	if len(got.Permissions) != 1 || got.Permissions[0] != user.Permissions[0] {
		t.Errorf("user permissions = %#v, want %#v", got.Permissions, user.Permissions)
	}
	if got.IsAdmin != user.IsAdmin {
		t.Errorf("user IsAdmin = %v, want %v", got.IsAdmin, user.IsAdmin)
	}

	svc.Delete("operator-2026")

	if _, ok := svc.GetUserById("operator-2026"); ok {
		t.Errorf("GetUserById(operator-2026) found deleted user")
	}
}

func TestAuthServiceValidate(t *testing.T) {
	svc := NewAuthService([]UserConfig{
		{
			ID:          "admin-2026",
			Name:        "Admin User",
			Permissions: []string{"/v1/tasks/**"},
			IsAdmin:     true,
		},
		{
			ID:          "viewer-2026",
			Name:        "Viewer User",
			Permissions: []string{"/v1/tasks", "/v1/tasks/:taskID", "/v1/tasks/*/result", "/v1/traces/**"},
		},
	})

	cases := []struct {
		name        string
		userID      string
		path        string
		wantErrPart string
	}{
		{
			name:   "admin-can-access-anything",
			userID: "admin-2026",
			path:   "/v1/settings/runtime",
		},
		{
			name:   "exact-path-permission",
			userID: "viewer-2026",
			path:   "/v1/tasks",
		},
		{
			name:   "path-parameter-permission",
			userID: "viewer-2026",
			path:   "/v1/tasks/task-20250226",
		},
		{
			name:   "single-level-wildcard-permission",
			userID: "viewer-2026",
			path:   "/v1/tasks/task-20250226/result",
		},
		{
			name:   "double-star-permission",
			userID: "viewer-2026",
			path:   "/v1/traces/task-20250226/detail",
		},
		{
			name:        "missing-user",
			userID:      "ghost-2026",
			path:        "/v1/tasks",
			wantErrPart: "not found",
		},
		{
			name:        "permission-denied",
			userID:      "viewer-2026",
			path:        "/v1/settings/runtime",
			wantErrPart: "does not have permission",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Validate(tc.userID, tc.path)
			if tc.wantErrPart == "" && err != nil {
				t.Errorf("Validate(%q, %q) error = %v", tc.userID, tc.path, err)
			}
			if tc.wantErrPart != "" {
				if err == nil {
					t.Errorf("Validate(%q, %q) error = nil, want %q", tc.userID, tc.path, tc.wantErrPart)
					return
				}
				if !strings.Contains(err.Error(), tc.wantErrPart) {
					t.Errorf("Validate(%q, %q) error = %q, want substring %q", tc.userID, tc.path, err.Error(), tc.wantErrPart)
				}
			}
		})
	}
}

func TestAuthServiceIsAdmin(t *testing.T) {
	svc := NewAuthService([]UserConfig{
		{ID: "admin-2026", Name: "Admin User", IsAdmin: true},
		{ID: "viewer-2026", Name: "Viewer User"},
	})

	if !svc.IsAdmin("admin-2026") {
		t.Errorf("IsAdmin(admin-2026) = false, want true")
	}
	if svc.IsAdmin("viewer-2026") {
		t.Errorf("IsAdmin(viewer-2026) = true, want false")
	}
	if svc.IsAdmin("ghost-2026") {
		t.Errorf("IsAdmin(ghost-2026) = true, want false")
	}
}

func TestAuthServiceMatchesPath(t *testing.T) {
	svc := NewAuthService(nil)

	cases := []struct {
		name       string
		permission string
		path       string
		want       bool
	}{
		{
			name:       "exact-match",
			permission: "/v1/tasks",
			path:       "/v1/tasks",
			want:       true,
		},
		{
			name:       "double-star-prefix",
			permission: "/v1/traces/**",
			path:       "/v1/traces/task-20250226/detail",
			want:       true,
		},
		{
			name:       "segment-mismatch",
			permission: "/v1/tasks/:taskID",
			path:       "/v1/config/runtime",
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.matchesPath(tc.permission, tc.path); got != tc.want {
				t.Errorf("matchesPath(%q, %q) = %v, want %v", tc.permission, tc.path, got, tc.want)
			}
		})
	}
}

func TestAuthServiceMatchesSegments(t *testing.T) {
	svc := NewAuthService(nil)

	cases := []struct {
		name       string
		permission string
		path       string
		want       bool
	}{
		{
			name:       "path-parameter",
			permission: "/v1/tasks/:taskID",
			path:       "/v1/tasks/task-20250226",
			want:       true,
		},
		{
			name:       "single-level-wildcard",
			permission: "/v1/tasks/*/result",
			path:       "/v1/tasks/task-20250226/result",
			want:       true,
		},
		{
			name:       "different-segment-length",
			permission: "/v1/tasks",
			path:       "/v1/tasks/task-20250226",
			want:       false,
		},
		{
			name:       "literal-segment-mismatch",
			permission: "/v1/tasks/*/result",
			path:       "/v1/tasks/task-20250226/summary",
			want:       false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.matchesSegments(tc.permission, tc.path); got != tc.want {
				t.Errorf("matchesSegments(%q, %q) = %v, want %v", tc.permission, tc.path, got, tc.want)
			}
		})
	}
}

func TestNewAuthMiddleware(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	svc := NewAuthService([]UserConfig{
		{
			ID:          "admin-2026",
			Name:        "Admin User",
			Permissions: []string{"/v1/tasks/**"},
			IsAdmin:     true,
		},
		{
			ID:          "viewer-2026",
			Name:        "Viewer User",
			Permissions: []string{"/v1/tasks/:taskID"},
		},
	})

	cases := []struct {
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
			name:         "missing-authorization-header",
			path:         "/v1/tasks/task-20250226",
			wantStatus:   http.StatusUnauthorized,
			wantBodyPart: "missing user ID",
		},
		{
			name:         "permission-denied",
			authHeader:   "viewer-2026",
			path:         "/v1/tasks/task-20250226/result",
			wantStatus:   http.StatusForbidden,
			wantBodyPart: "does not have permission",
		},
		{
			name:           "authorized-admin-request",
			authHeader:     "admin-2026",
			path:           "/v1/tasks/task-20250226/result",
			wantStatus:     http.StatusNoContent,
			wantHandlerRun: true,
			wantUserID:     "admin-2026",
			wantIsAdmin:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := httpGin.New()

			var handlerRan bool
			var gotUserID string
			var gotIsAdmin bool

			engine.GET(
				"/v1/tasks/:taskID",
				wrapHandler(NewAuthMiddleware(svc, nil)),
				wrapHandler(func(ctx *Context) {
					handlerRan = true
					gotUserID = ctx.UserID
					gotIsAdmin = ctx.IsAdmin
					ctx.Status(http.StatusNoContent)
				}),
			)
			engine.GET(
				"/v1/tasks/:taskID/result",
				wrapHandler(NewAuthMiddleware(svc, nil)),
				wrapHandler(func(ctx *Context) {
					handlerRan = true
					gotUserID = ctx.UserID
					gotIsAdmin = ctx.IsAdmin
					ctx.Status(http.StatusNoContent)
				}),
			)

			request := httptest.NewRequest(http.MethodGet, tc.path, http.NoBody)
			if tc.authHeader != "" {
				request.Header.Set("Authorization", tc.authHeader)
			}
			recorder := httptest.NewRecorder()

			engine.ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Errorf("response status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if tc.wantBodyPart != "" && !strings.Contains(recorder.Body.String(), tc.wantBodyPart) {
				t.Errorf("response body = %q, want substring %q", recorder.Body.String(), tc.wantBodyPart)
			}
			if handlerRan != tc.wantHandlerRun {
				t.Errorf("handler executed = %v, want %v", handlerRan, tc.wantHandlerRun)
			}
			if tc.wantHandlerRun {
				if gotUserID != tc.wantUserID {
					t.Errorf("ctx.UserID = %q, want %q", gotUserID, tc.wantUserID)
				}
				if gotIsAdmin != tc.wantIsAdmin {
					t.Errorf("ctx.IsAdmin = %v, want %v", gotIsAdmin, tc.wantIsAdmin)
				}
			}
		})
	}
}
