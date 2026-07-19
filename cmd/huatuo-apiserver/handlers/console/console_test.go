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

package console

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/version"
)

// buildTestServer wires a real server with the given users and the console
// routes so the full middleware stack (auth + routing) is exercised end to end.
func buildTestServer(t *testing.T, users []server.UserConfig) (*httptest.Server, server.UserManager) {
	t.Helper()
	info := &version.Info{Version: "9.9.9", GitCommit: "testcommit"}
	httpServer := server.NewServer(&server.Config{
		AuthUsers:   users,
		PublicPaths: []string{"/console"},
		VersionInfo: info,
	})
	um := httpServer.UserManager()
	httpServer.MustRegisterRoutes("/v1", NewHandler(um, info).Handlers)
	httpServer.StaticFS("/console", WebFS())
	ts := httptest.NewServer(httpServer.HTTPHandler())
	t.Cleanup(ts.Close)
	return ts, um
}

func do(t *testing.T, ts *httptest.Server, method, path, auth, body string) (*http.Response, []byte) {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	payload, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, payload
}

func decodeEnvelope(t *testing.T, payload []byte, target any) {
	t.Helper()
	var env struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, string(payload))
	}
	if env.Code != 0 {
		t.Fatalf("envelope code = %d, body=%s", env.Code, string(payload))
	}
	if target != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, target); err != nil {
			t.Fatalf("decode data: %v; body=%s", err, string(env.Data))
		}
	}
}

func TestRoleCatalogContainsAdminOperatorViewer(t *testing.T) {
	roles := roleCatalog()
	names := map[string]bool{}
	for _, r := range roles {
		names[r.Name] = true
	}
	for _, want := range []string{"admin", "operator", "viewer"} {
		if !names[want] {
			t.Errorf("role catalog missing %q; got %v", want, names)
		}
	}
}

func TestNormalizePermissionsDedupsAndTrims(t *testing.T) {
	got := normalizePermissions([]string{" /v1/traces ", "", "/v1/traces", "/v1/profiles"})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (%+v)", len(got), got)
	}
}

func TestGenerateAPIKeyUniqueAndPrefixed(t *testing.T) {
	a, err := generateAPIKey()
	if err != nil {
		t.Fatalf("generateAPIKey() error = %v", err)
	}
	b, err := generateAPIKey()
	if err != nil {
		t.Fatalf("generateAPIKey() error = %v", err)
	}
	if !strings.HasPrefix(a, "htk_") || !strings.HasPrefix(b, "htk_") {
		t.Errorf("keys missing prefix: %q %q", a, b)
	}
	if a == b {
		t.Errorf("generated keys are identical: %q", a)
	}
}

func TestMaskKey(t *testing.T) {
	if got := maskKey("htk_0123456789abcdef"); got != "htk_...cdef" {
		t.Errorf("maskKey() = %q, want htk_...cdef", got)
	}
	if got := maskKey("short"); got == "short" {
		t.Errorf("maskKey() did not mask short key: %q", got)
	}
}

func TestDefaultModulesCoverThreeModules(t *testing.T) {
	mods := defaultModules()
	names := map[string]bool{}
	for _, m := range mods {
		names[m.Name] = true
		if !m.Enabled {
			t.Errorf("module %q not enabled", m.Name)
		}
	}
	for _, want := range []string{"metrics", "tracing", "profiling"} {
		if !names[want] {
			t.Errorf("defaultModules missing %q; got %v", want, names)
		}
	}
}

func TestWebFSEmbedsAssets(t *testing.T) {
	fsys := WebFS()
	idx, err := fsys.Open("index.html")
	if err != nil {
		t.Fatalf("open index.html: %v", err)
	}
	defer idx.Close()
	data, err := io.ReadAll(idx)
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(data), "HuaTuo Console") {
		t.Errorf("index.html missing title")
	}
	if _, err := fsys.Open("assets/app.js"); err != nil {
		t.Errorf("open assets/app.js: %v", err)
	}
	if _, err := fsys.Open("assets/app.css"); err != nil {
		t.Errorf("open assets/app.css: %v", err)
	}
}

func TestSystemInfoReturnsConfigLimits(t *testing.T) {
	cfg := config.Get()
	old := cfg.TaskConfig
	cfg.TaskConfig.MaxProfilingTasksPerHost = 7
	cfg.TaskConfig.MaxTracingTasksPerHost = 9
	cfg.TaskConfig.MaxTotalProfilingTasks = 11
	cfg.TaskConfig.MaxTotalTracingTasks = 13
	defer func() { cfg.TaskConfig = old }()

	ts, _ := buildTestServer(t, []server.UserConfig{
		{ID: "admin-2026", Name: "Admin", IsAdmin: true},
	})
	resp, payload := do(t, ts, http.MethodGet, "/v1/system/info", "admin-2026", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, string(payload))
	}
	var info v1.SystemInfoResponse
	decodeEnvelope(t, payload, &info)
	if info.Limits.MaxProfilingTasksPerHost != 7 {
		t.Errorf("MaxProfilingTasksPerHost = %d, want 7", info.Limits.MaxProfilingTasksPerHost)
	}
	if info.Limits.MaxTracingTasksPerHost != 9 {
		t.Errorf("MaxTracingTasksPerHost = %d, want 9", info.Limits.MaxTracingTasksPerHost)
	}
	if info.Version != "9.9.9" {
		t.Errorf("Version = %q, want 9.9.9", info.Version)
	}
}

func TestWhoamiReturnsCurrentUserIdentity(t *testing.T) {
	ts, _ := buildTestServer(t, []server.UserConfig{
		{ID: "viewer-2026", Name: "Viewer", Permissions: []string{"/v1/traces", "/v1/auth/whoami"}},
	})
	resp, payload := do(t, ts, http.MethodGet, "/v1/auth/whoami", "viewer-2026", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.StatusCode, string(payload))
	}
	var who v1.WhoAmIResponse
	decodeEnvelope(t, payload, &who)
	if who.ID != "viewer-2026" || who.Name != "Viewer" {
		t.Errorf("whoami = %+v", who)
	}
	if who.IsAdmin {
		t.Errorf("whoami IsAdmin = true, want false")
	}
}

func TestWhoamiRejectsUnknownKey(t *testing.T) {
	ts, _ := buildTestServer(t, []server.UserConfig{
		{ID: "admin-2026", Name: "Admin", IsAdmin: true},
	})
	resp, _ := do(t, ts, http.MethodGet, "/v1/auth/whoami", "ghost", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unknown key status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestListUsersAdminOnly(t *testing.T) {
	ts, _ := buildTestServer(t, []server.UserConfig{
		{ID: "admin-2026", Name: "Admin", IsAdmin: true},
		{ID: "viewer-2026", Name: "Viewer", Permissions: []string{"/v1/users"}},
	})

	if resp, _ := do(t, ts, http.MethodGet, "/v1/users", "viewer-2026", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin listUsers status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	resp, payload := do(t, ts, http.MethodGet, "/v1/users", "admin-2026", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin listUsers status = %d; body=%s", resp.StatusCode, string(payload))
	}
	var users []v1.UserResponse
	decodeEnvelope(t, payload, &users)
	if len(users) != 2 {
		t.Errorf("users len = %d, want 2", len(users))
	}
}

func TestCreateUserGeneratesKeyAndEnforcesAdmin(t *testing.T) {
	ts, _ := buildTestServer(t, []server.UserConfig{
		{ID: "admin-2026", Name: "Admin", IsAdmin: true},
	})

	// Non-admin cannot create (no auth -> unauthorized at middleware).
	if resp, _ := do(t, ts, http.MethodPost, "/v1/users", "nobody", `{"name":"x","generate_key":true}`); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin create status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	resp, payload := do(t, ts, http.MethodPost, "/v1/users", "admin-2026", `{"name":"Operator","generate_key":true,"permissions":["/v1/traces"]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", resp.StatusCode, string(payload))
	}

	var created v1.CreateUserResponse
	decodeEnvelope(t, payload, &created)
	if !strings.HasPrefix(created.ID, "htk_") {
		t.Errorf("generated id = %q, want htk_ prefix", created.ID)
	}

	// Duplicate explicit ID conflicts.
	body := `{"name":"Dup","generate_key":false,"id":"` + created.ID + `"}`
	if resp, _ := do(t, ts, http.MethodPost, "/v1/users", "admin-2026", body); resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate create status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
}

func TestCreateUserRequiresName(t *testing.T) {
	ts, _ := buildTestServer(t, []server.UserConfig{
		{ID: "admin-2026", Name: "Admin", IsAdmin: true},
	})
	if resp, _ := do(t, ts, http.MethodPost, "/v1/users", "admin-2026", `{"name":"","generate_key":true}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing name status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestDeleteUserAdminOnlyAndCannotDeleteSelf(t *testing.T) {
	ts, _ := buildTestServer(t, []server.UserConfig{
		{ID: "admin-2026", Name: "Admin", IsAdmin: true},
		{ID: "viewer-2026", Name: "Viewer", Permissions: []string{"/v1/users"}},
	})

	// Non-admin cannot delete.
	if resp, _ := do(t, ts, http.MethodDelete, "/v1/users/viewer-2026", "viewer-2026", ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin delete status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	// Admin cannot delete self.
	if resp, _ := do(t, ts, http.MethodDelete, "/v1/users/admin-2026", "admin-2026", ""); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("delete self status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	// Admin can delete another user.
	resp, _ := do(t, ts, http.MethodDelete, "/v1/users/viewer-2026", "admin-2026", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}

func TestListRolesOpenToAnyAuthenticatedUser(t *testing.T) {
	ts, _ := buildTestServer(t, []server.UserConfig{
		{ID: "viewer-2026", Name: "Viewer", Permissions: []string{"/v1/roles"}},
	})
	resp, payload := do(t, ts, http.MethodGet, "/v1/roles", "viewer-2026", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body=%s", resp.StatusCode, string(payload))
	}
	var roles []v1.RoleResponse
	decodeEnvelope(t, payload, &roles)
	if len(roles) == 0 {
		t.Errorf("roles empty")
	}
}

func TestConsoleAssetsServedWithoutAuth(t *testing.T) {
	ts, _ := buildTestServer(t, []server.UserConfig{
		{ID: "admin-2026", Name: "Admin", IsAdmin: true},
	})
	resp, body := do(t, ts, http.MethodGet, "/console/", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !strings.Contains(string(body), "HuaTuo Console") {
		t.Errorf("index body does not contain title")
	}

	resp, _ = do(t, ts, http.MethodGet, "/console/assets/app.css", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("app.css status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}
