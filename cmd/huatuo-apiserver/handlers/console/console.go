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

// Package console implements the access-control (RBAC), system-info and API-key
// management endpoints that back the huatuo-apiserver web console. It also serves
// the embedded single-page frontend.
package console

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/internal/version"
)

// apiKeyBytes is the length of generated API keys, in bytes (32 hex chars).
const apiKeyBytes = 16

// Handler handles RBAC, system-info and API-key management HTTP requests.
type Handler struct {
	userManager server.UserManager
	versionInfo *version.Info
	Handlers    []server.Handle
}

// NewHandler creates a new console handler.
// userManager may be nil when authentication is disabled; in that case the
// user/role management endpoints report that authentication is disabled.
func NewHandler(userManager server.UserManager, versionInfo *version.Info) *Handler {
	h := &Handler{
		userManager: userManager,
		versionInfo: versionInfo,
	}

	h.Handlers = []server.Handle{
		{Typ: server.HttpGet, Uri: "/auth/whoami", Handle: h.whoami},
		{Typ: server.HttpGet, Uri: "/roles", Handle: h.listRoles},
		{Typ: server.HttpGet, Uri: "/users", Handle: h.listUsers},
		{Typ: server.HttpPost, Uri: "/users", Handle: h.createUser},
		{Typ: server.HttpDelete, Uri: "/users/:id", Handle: h.deleteUser},
		{Typ: server.HttpGet, Uri: "/system/info", Handle: h.systemInfo},
	}

	return h
}

// whoami returns the identity of the authenticated caller. Any valid user may
// call this; it powers the console login verification step.
func (h *Handler) whoami(ctx *server.Context) error {
	if h.userManager == nil {
		response.Success(ctx, v1.WhoAmIResponse{})
		return nil
	}

	user, ok := h.userManager.GetUserById(ctx.UserID)
	if !ok {
		return response.ErrUnauthorized.WithMessage("user not found")
	}

	perms := make([]string, 0, len(user.Permissions))
	for _, p := range user.Permissions {
		perms = append(perms, string(p))
	}

	response.Success(ctx, v1.WhoAmIResponse{
		ID:          user.ID,
		Name:        user.Name,
		IsAdmin:     user.IsAdmin,
		Permissions: perms,
	})
	return nil
}

// listUsers returns all registered users/API keys. Administrator only.
func (h *Handler) listUsers(ctx *server.Context) error {
	if !ctx.IsAdmin {
		return response.ErrForbidden
	}
	if h.userManager == nil {
		response.Success(ctx, []v1.UserResponse{})
		return nil
	}

	users := h.userManager.ListUsers()
	items := make([]v1.UserResponse, 0, len(users))
	for _, u := range users {
		items = append(items, toUserResponse(u))
	}

	response.Success(ctx, items)
	return nil
}

// createUser registers a new user/API key. Administrator only.
func (h *Handler) createUser(ctx *server.Context) error {
	if !ctx.IsAdmin {
		return response.ErrForbidden
	}
	if h.userManager == nil {
		return response.ErrInvalidRequest.WithMessage("authentication is disabled")
	}

	var req v1.CreateUserRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		return response.ErrInvalidRequest.WithMessage(err.Error())
	}

	if strings.TrimSpace(req.Name) == "" {
		return response.ErrInvalidRequest.WithMessage("name is required")
	}

	id := strings.TrimSpace(req.ID)
	if req.GenerateKey || id == "" {
		generated, err := generateAPIKey()
		if err != nil {
			log.Errorf("Failed to generate API key: %v", err)
			return response.ErrInternal.WithMessage("failed to generate API key")
		}
		id = generated
	}

	if _, exists := h.userManager.GetUserById(id); exists {
		return response.ErrConflict.WithMessage("user ID already exists")
	}

	permissions := normalizePermissions(req.Permissions)
	applyRolePermissions(&permissions, req.IsAdmin)

	user := server.User{
		ID:          id,
		Name:        req.Name,
		Permissions: permissions,
		IsAdmin:     req.IsAdmin,
	}
	h.userManager.Add(user)

	log.Infof("Created API key %q (name=%q, admin=%v) from console", maskKey(id), req.Name, req.IsAdmin)

	response.Created(ctx, "/v1/users/"+id, v1.CreateUserResponse{ID: id})
	return nil
}

// deleteUser removes a user/API key. Administrator only. The current admin may
// not delete their own key, to avoid locking everyone out.
func (h *Handler) deleteUser(ctx *server.Context) error {
	if !ctx.IsAdmin {
		return response.ErrForbidden
	}
	if h.userManager == nil {
		return response.ErrInvalidRequest.WithMessage("authentication is disabled")
	}

	id := ctx.Param("id")
	if id == "" {
		return response.ErrInvalidRequest.WithMessage("id is required")
	}
	if id == ctx.UserID {
		return response.ErrInvalidRequest.WithMessage("cannot delete your own API key")
	}

	if _, exists := h.userManager.GetUserById(id); !exists {
		return response.ErrNotFound.WithMessage("user not found")
	}

	h.userManager.Delete(id)
	log.Infof("Deleted API key %q from console", maskKey(id))

	response.NoContent(ctx)
	return nil
}

// listRoles returns the role/permission catalog that the console offers when
// provisioning API keys.
func (h *Handler) listRoles(ctx *server.Context) error {
	response.Success(ctx, roleCatalog())
	return nil
}

// systemInfo returns aggregated status used by the dashboard.
func (h *Handler) systemInfo(ctx *server.Context) error {
	cfg := config.Get()

	resp := v1.SystemInfoResponse{
		Modules: defaultModules(),
		Limits: v1.SystemLimits{
			MaxProfilingTasksPerHost: cfg.TaskConfig.MaxProfilingTasksPerHost,
			MaxTracingTasksPerHost:   cfg.TaskConfig.MaxTracingTasksPerHost,
			MaxTotalProfilingTasks:   cfg.TaskConfig.MaxTotalProfilingTasks,
			MaxTotalTracingTasks:     cfg.TaskConfig.MaxTotalTracingTasks,
		},
	}

	if h.versionInfo != nil {
		resp.Version = h.versionInfo.Version
		resp.Commit = h.versionInfo.GitCommit
	}

	response.Success(ctx, resp)
	return nil
}

// toUserResponse converts an internal server.User to the v1 wire type.
func toUserResponse(u server.User) v1.UserResponse {
	perms := make([]string, 0, len(u.Permissions))
	for _, p := range u.Permissions {
		perms = append(perms, string(p))
	}
	return v1.UserResponse{
		ID:          u.ID,
		Name:        u.Name,
		IsAdmin:     u.IsAdmin,
		Permissions: perms,
	}
}

// normalizePermissions trims and de-duplicates permission patterns.
func normalizePermissions(perms []string) []server.Permission {
	seen := make(map[string]bool, len(perms))
	out := make([]server.Permission, 0, len(perms))
	for _, p := range perms {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, server.Permission(p))
	}
	return out
}

// applyRolePermissions ensures admins implicitly have access to everything and
// that every user can at least read the console metadata endpoints.
func applyRolePermissions(perms *[]server.Permission, isAdmin bool) {
	if isAdmin {
		*perms = nil
		return
	}
	required := []string{"/v1/auth/whoami", "/v1/system/info", "/v1/profiles/capabilities", "/v1/roles"}
	existing := make(map[string]bool, len(*perms))
	for _, p := range *perms {
		existing[string(p)] = true
	}
	for _, p := range required {
		if !existing[p] {
			*perms = append(*perms, server.Permission(p))
		}
	}
}

// generateAPIKey returns a cryptographically random hex string suitable for use
// as the Authorization credential / API key.
func generateAPIKey() (string, error) {
	b := make([]byte, apiKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return "htk_" + hex.EncodeToString(b), nil
}

// maskKey returns a redacted form of an API key for log lines.
func maskKey(id string) string {
	if len(id) <= 8 {
		return strings.Repeat("*", len(id))
	}
	return id[:4] + "..." + id[len(id)-4:]
}

// defaultModules describes the integrated modules shown in the console.
func defaultModules() []v1.ModuleInfo {
	return []v1.ModuleInfo{
		{
			Name:        "metrics",
			DisplayName: "Metrics",
			Description: "Kernel-wide Prometheus metrics exposed by the apiserver.",
			Endpoint:    "/metrics",
			Enabled:     true,
		},
		{
			Name:        "tracing",
			DisplayName: "Event Tracing",
			Description: "On-demand distributed tracing jobs orchestrated across hosts.",
			Endpoint:    "/v1/traces",
			Enabled:     true,
		},
		{
			Name:        "profiling",
			DisplayName: "Continuous Profiling",
			Description: "CPU and memory profiling with flame-graph integration.",
			Endpoint:    "/v1/profiles",
			Enabled:     true,
		},
	}
}

// roleCatalog returns the role templates offered by the console. The catalog is
// path-based: a role grants the listed URL path patterns. Administrator roles
// bypass path checks entirely.
func roleCatalog() []v1.RoleResponse {
	return []v1.RoleResponse{
		{
			Name:        "admin",
			Description: "Full access. Permissions are ignored because IsAdmin is set.",
			IsAdmin:     true,
		},
		{
			Name:        "operator",
			Description: "Read and write access to tracing, profiling and console metadata.",
			Permissions: []string{"/v1/traces", "/v1/traces/**", "/v1/profiles", "/v1/profiles/**"},
		},
		{
			Name:        "viewer",
			Description: "Read-only access to job lists, details and capabilities.",
			Permissions: []string{"/v1/traces", "/v1/traces/:id", "/v1/profiles", "/v1/profiles/:id"},
		},
	}
}
