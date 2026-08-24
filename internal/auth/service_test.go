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

package auth

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceAuthenticateBearer(t *testing.T) {
	t.Parallel()

	service := newTestService()
	tests := []struct {
		name    string
		header  string
		wantID  string
		wantErr error
	}{
		{name: "valid", header: "Bearer viewer-secret", wantID: "viewer-2026"},
		{name: "case insensitive scheme", header: "bearer viewer-secret", wantID: "viewer-2026"},
		{name: "missing", wantErr: ErrMissingBearerToken},
		{name: "wrong scheme", header: "Basic viewer-secret", wantErr: ErrMissingBearerToken},
		{name: "empty token", header: "Bearer ", wantErr: ErrMissingBearerToken},
		{name: "invalid", header: "Bearer unknown", wantErr: ErrInvalidBearerToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			principal, err := service.AuthenticateBearer(tt.header)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantID, principal.ID)
		})
	}
}

func TestServiceAuthenticateReturnsCopy(t *testing.T) {
	t.Parallel()

	service := newTestService()
	principal, ok := service.Authenticate("viewer-secret")
	require.True(t, ok)
	principal.Permissions[0] = "DELETE /v1/**"

	fresh, ok := service.Authenticate("viewer-secret")
	require.True(t, ok)
	require.Equal(t, Permission("GET /v1/profiling/**"), fresh.Permissions[0])
}

func TestServiceAuthorize(t *testing.T) {
	t.Parallel()

	service := newTestService()
	viewer, ok := service.Authenticate("viewer-secret")
	require.True(t, ok)
	admin, ok := service.Authenticate("admin-secret")
	require.True(t, ok)

	require.NoError(t, service.Authorize(viewer, http.MethodGet, "/v1/profiling/job-2026"))
	require.NoError(t, service.Authorize(viewer, http.MethodGet, "/v1/tracing/job-2026"))
	err := service.Authorize(viewer, http.MethodPost, "/v1/profiling")
	require.ErrorIs(t, err, ErrPermissionDenied)
	require.EqualError(
		t,
		err,
		`permission denied: user "viewer-2026" does not have permission to access POST /v1/profiling`,
	)
	require.NoError(t, service.Authorize(admin, http.MethodDelete, "/v1/settings"))
}

func TestRequireAdministrator(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, RequireAdministrator(Principal{}), ErrAdministratorRequired)
	require.NoError(t, RequireAdministrator(Principal{IsAdmin: true}))
}

func TestMatchesPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		permission string
		path       string
		want       bool
	}{
		{name: "exact", permission: "/v1/tracing", path: "/v1/tracing", want: true},
		{name: "single star", permission: "/v1/tracing/*", path: "/v1/tracing/id", want: true},
		{name: "parameter", permission: "/v1/tracing/:id", path: "/v1/tracing/id", want: true},
		{name: "recursive", permission: "/v1/tracing/**", path: "/v1/tracing/id/result", want: true},
		{name: "recursive needs descendant", permission: "/v1/tracing/**", path: "/v1/tracing"},
		{name: "recursive keeps suffix", permission: "/v1/**/stop", path: "/v1/tracing/id/stop", want: true},
		{name: "recursive rejects suffix", permission: "/v1/**/stop", path: "/v1/tracing/id/result"},
		{name: "reject sibling prefix", permission: "/v1/tracing/**", path: "/v1/tracing-old/id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, MatchesPath(tt.permission, tt.path))
		})
	}

	require.True(t, MatchesAnyPath([]string{"/healthz", "/v1/tracing/**"}, "/healthz"))
	require.False(t, MatchesAnyPath([]string{"/healthz", "/v1/tracing/**"}, "/readyz"))
}

func newTestService() *Service {
	return NewService([]UserConfig{
		{
			ID:          "admin-2026",
			BearerToken: "admin-secret",
			IsAdmin:     true,
		},
		{
			ID:          "viewer-2026",
			BearerToken: "viewer-secret",
			Permissions: []string{
				"GET /v1/profiling/**",
				"/v1/tracing/:id",
			},
		},
	})
}

func TestErrorsRemainDistinct(t *testing.T) {
	t.Parallel()

	require.False(t, errors.Is(ErrMissingBearerToken, ErrInvalidBearerToken))
}
