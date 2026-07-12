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
)

func TestServerUserManagerNilWithoutAuth(t *testing.T) {
	s := NewServer(nil)
	if um := s.UserManager(); um != nil {
		t.Errorf("UserManager() = %v, want nil when auth disabled", um)
	}
}

func TestServerUserManagerReturnsConfiguredUsers(t *testing.T) {
	s := NewServer(&Config{
		AuthUsers: []UserConfig{
			{ID: "admin-2026", Name: "Admin", IsAdmin: true},
			{ID: "viewer-2026", Name: "Viewer", Permissions: []string{"/v1/traces"}},
		},
	})

	um := s.UserManager()
	if um == nil {
		t.Fatalf("UserManager() = nil, want non-nil")
	}
	users := um.ListUsers()
	if len(users) != 2 {
		t.Fatalf("ListUsers() len = %d, want 2", len(users))
	}
	if !um.IsAdmin("admin-2026") {
		t.Errorf("IsAdmin(admin-2026) = false, want true")
	}
	if um.IsAdmin("viewer-2026") {
		t.Errorf("IsAdmin(viewer-2026) = true, want false")
	}
}

func TestServerUserManagerMutationsAreVisibleToAuth(t *testing.T) {
	s := NewServer(&Config{
		AuthUsers: []UserConfig{
			{ID: "admin-2026", Name: "Admin", IsAdmin: true},
		},
		PublicPaths: []string{"/console"},
	})
	um := s.UserManager()

	// Add a new API key and confirm it is accepted by the middleware.
	um.Add(User{
		ID:          "operator-2026",
		Name:        "Operator",
		Permissions: []Permission{"/v1/traces"},
	})

	s.MustRegisterRoutes("/v1/traces", []Handle{
		{Typ: HttpGet, Uri: "", Handle: func(ctx *Context) error {
			ctx.Status(http.StatusNoContent)
			return nil
		}},
	})

	cases := []struct {
		name       string
		authHeader string
		target     string
		wantStatus int
	}{
		{name: "new-key-accepted", authHeader: "operator-2026", target: "/v1/traces", wantStatus: http.StatusNoContent},
		{name: "missing-key-rejected", authHeader: "", target: "/v1/traces", wantStatus: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, http.NoBody)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			s.engine.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}

	// Deleting the key revokes access.
	um.Delete("operator-2026")
	req := httptest.NewRequest(http.MethodGet, "/v1/traces", http.NoBody)
	req.Header.Set("Authorization", "operator-2026")
	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("after delete status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestServerPublicPathServedWithoutAuth(t *testing.T) {
	s := NewServer(&Config{
		AuthUsers:   []UserConfig{{ID: "admin-2026", Name: "Admin", IsAdmin: true}},
		PublicPaths: []string{"/console"},
	})
	s.MustRegisterRoutes("", []Handle{
		{Typ: HttpGet, Uri: "/console", Handle: func(ctx *Context) error {
			ctx.JSON(http.StatusOK, map[string]string{"ok": "true"})
			return nil
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/console", http.NoBody)
	rec := httptest.NewRecorder()
	s.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("public path status = %d, want %d", rec.Code, http.StatusOK)
	}
}
