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
	"testing"

	httpGin "github.com/gin-gonic/gin"
)

func TestAuthServiceListUsers(t *testing.T) {
	svc := NewAuthService([]UserConfig{
		{ID: "beta-2026", Name: "Beta", IsAdmin: true},
		{ID: "alpha-2026", Name: "Alpha", Permissions: []string{"/v1/traces"}},
	})

	users := svc.ListUsers()
	if len(users) != 2 {
		t.Fatalf("ListUsers() len = %d, want 2", len(users))
	}
	if users[0].ID != "alpha-2026" || users[1].ID != "beta-2026" {
		t.Errorf("ListUsers() not sorted by ID: %+v", users)
	}
}

func TestAuthServiceImplementsUserManager(t *testing.T) {
	var _ UserManager = (*authService)(nil)
}

func TestIsPublicPath(t *testing.T) {
	public := []string{"/", "/console"}

	cases := []struct {
		path string
		want bool
	}{
		{"/", true},
		{"/console", true},
		{"/console/", true},
		{"/console/assets/app.css", true},
		{"/v1/traces", false},
		{"/consolefoo", false}, // prefix must be followed by "/"
		{"/healthz", false},
	}

	for _, tc := range cases {
		if got := isPublicPath(tc.path, public); got != tc.want {
			t.Errorf("isPublicPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsPublicPathEmptyPrefix(t *testing.T) {
	if isPublicPath("/anything", []string{""}) {
		t.Errorf("empty prefix should not exempt any path")
	}
}

func TestNewAuthMiddlewarePublicPathBypass(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	svc := NewAuthService([]UserConfig{
		{ID: "admin-2026", Name: "Admin", IsAdmin: true},
	})

	var handlerRan bool
	engine := httpGin.New()
	engine.GET(
		"/console/*filepath",
		wrapHandler(NewAuthMiddleware(svc, []string{"/console"})),
		wrapHandler(func(ctx *Context) {
			handlerRan = true
			ctx.Status(http.StatusNoContent)
		}),
	)

	// No Authorization header, but the path is public.
	request := httptest.NewRequest(http.MethodGet, "/console/assets/app.css", http.NoBody)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Errorf("public path status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if !handlerRan {
		t.Errorf("public path handler did not run")
	}
}

func TestNewAuthMiddlewareNonPublicStillRequiresAuth(t *testing.T) {
	httpGin.SetMode(httpGin.TestMode)

	svc := NewAuthService([]UserConfig{
		{ID: "admin-2026", Name: "Admin", IsAdmin: true},
	})

	engine := httpGin.New()
	engine.GET(
		"/v1/users",
		wrapHandler(NewAuthMiddleware(svc, []string{"/console"})),
		wrapHandler(func(ctx *Context) {
			ctx.Status(http.StatusNoContent)
		}),
	)

	request := httptest.NewRequest(http.MethodGet, "/v1/users", http.NoBody)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("protected path without auth status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}
