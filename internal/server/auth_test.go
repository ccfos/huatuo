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

// TestAuthServiceAndMiddleware 覆盖鉴权服务的主要能力，验证了用户初始化、Add/Delete/Get、精确路径和通配权限匹配、管理员判断，以及中间件在缺少 Authorization、无权限、和有权限三种请求下的响应行为。
func TestAuthServiceAndMiddleware(t *testing.T) {
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
			Permissions: []string{"/v1/tasks", "/v1/tasks/:taskID", "/v1/tasks/*/result"},
		},
	})

	adminUser, ok := svc.GetUserById("admin-2026")
	if !ok {
		t.Errorf("GetUserById(admin-2026) did not find user")
	} else {
		if !adminUser.IsAdmin {
			t.Errorf("admin user IsAdmin = false, want true")
		}
		if len(adminUser.Permissions) != 1 || adminUser.Permissions[0] != Permission("/v1/tasks/**") {
			t.Errorf("admin permissions = %#v, want [/v1/tasks/**]", adminUser.Permissions)
		}
	}

	svc.Add(User{
		ID:          "operator-2026",
		Name:        "Operator User",
		Permissions: []Permission{"/v1/config"},
	})
	operator, ok := svc.GetUserById("operator-2026")
	if !ok {
		t.Errorf("GetUserById(operator-2026) did not find user after Add")
	} else if operator.Name != "Operator User" {
		t.Errorf("operator name = %q, want %q", operator.Name, "Operator User")
	}
	svc.Delete("operator-2026")
	if _, ok := svc.GetUserById("operator-2026"); ok {
		t.Errorf("GetUserById(operator-2026) found deleted user")
	}

	validateCases := []struct {
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

	for _, tc := range validateCases {
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

	matchesCases := []struct {
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
		{
			name:       "single-level-wildcard",
			permission: "/v1/tasks/*/result",
			path:       "/v1/tasks/task-20250226/result",
			want:       true,
		},
	}

	for _, tc := range matchesCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.matchesPath(tc.permission, tc.path); got != tc.want {
				t.Errorf("matchesPath(%q, %q) = %v, want %v", tc.permission, tc.path, got, tc.want)
			}
		})
	}

	if !svc.IsAdmin("admin-2026") {
		t.Errorf("IsAdmin(admin-2026) = false, want true")
	}
	if svc.IsAdmin("viewer-2026") {
		t.Errorf("IsAdmin(viewer-2026) = true, want false")
	}
	if svc.IsAdmin("ghost-2026") {
		t.Errorf("IsAdmin(ghost-2026) = true, want false")
	}

	httpGin.SetMode(httpGin.TestMode)
	engine := httpGin.New()
	engine.Use(middlewareContext(), wrapHandler(NewAuthMiddleware(svc)))
	engine.GET("/v1/tasks/:taskID", func(c *httpGin.Context) {
		c.JSON(http.StatusOK, map[string]string{"taskID": c.Param("taskID")})
	})

	middlewareCases := []struct {
		name         string
		headerValue  string
		wantStatus   int
		wantBodyPart string
	}{
		{
			name:         "missing-authorization",
			wantStatus:   http.StatusUnauthorized,
			wantBodyPart: "missing user ID",
		},
		{
			name:         "forbidden-user",
			headerValue:  "ghost-2026",
			wantStatus:   http.StatusForbidden,
			wantBodyPart: "not found",
		},
		{
			name:         "authorized-user",
			headerValue:  "viewer-2026",
			wantStatus:   http.StatusOK,
			wantBodyPart: `"taskID":"task-20250226"`,
		},
	}

	for _, tc := range middlewareCases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-20250226", http.NoBody)
			if tc.headerValue != "" {
				request.Header.Set("Authorization", tc.headerValue)
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Errorf("middleware response status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if !strings.Contains(recorder.Body.String(), tc.wantBodyPart) {
				t.Errorf("middleware response body = %q, want substring %q", recorder.Body.String(), tc.wantBodyPart)
			}
		})
	}
}
