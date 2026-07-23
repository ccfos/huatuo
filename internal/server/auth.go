// Copyright 2025, 2026 The HuaTuo Authors
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
	"fmt"
	"net/http"
	"strings"
	"sync"

	"huatuo-bamai/internal/server/response"
)

// Permission represents a permission string.
type Permission string

// User represents a user with permissions.
type User struct {
	ID          string
	Name        string
	Permissions []Permission
	IsAdmin     bool
}

// UserConfig represents a user configuration for initialization.
type UserConfig struct {
	ID          string
	Name        string
	Permissions []string
	IsAdmin     bool
}

// authService handles authentication and authorization.
type authService struct {
	users sync.Map
}

// NewService creates a new auth authService.
func NewAuthService(users []UserConfig) *authService {
	s := &authService{users: sync.Map{}}

	for _, cfgUser := range users {
		permissions := make([]Permission, 0, len(cfgUser.Permissions))
		for _, p := range cfgUser.Permissions {
			permissions = append(permissions, Permission(p))
		}

		s.users.Store(cfgUser.ID, User{
			ID:          cfgUser.ID,
			Name:        cfgUser.Name,
			Permissions: permissions,
			IsAdmin:     cfgUser.IsAdmin,
		})
	}

	return s
}

// Add adds a user to the authService.
func (s *authService) Add(user User) {
	s.users.Store(user.ID, user)
}

// Delete removes a user from the authService.
func (s *authService) Delete(userID string) {
	s.users.Delete(userID)
}

// GetUserByID gets a user by ID.
func (s *authService) GetUserByID(userID string) (User, bool) {
	value, exists := s.users.Load(userID)
	if !exists {
		return User{}, false
	}
	return value.(User), true
}

// Validate validates if a user has access to a specific path.
func (s *authService) Validate(userID string, request ...string) error {
	method, path := "", ""
	if len(request) == 1 {
		path = request[0]
	} else if len(request) >= 2 {
		method, path = request[0], request[1]
	}
	value, exists := s.users.Load(userID)
	if !exists {
		return fmt.Errorf("user %s not found", userID)
	}

	user := value.(User)

	// Admin has access to everything
	if user.IsAdmin {
		return nil
	}

	// Check if user has permission for this path
	for _, perm := range user.Permissions {
		permissionMethod, permissionPath := splitPermission(string(perm))
		if (permissionMethod == "" || permissionMethod == method) && s.matchesPath(permissionPath, path) {
			return nil
		}
	}

	return fmt.Errorf("user does not have permission to access %s %s", method, path)
}

func splitPermission(permission string) (string, string) {
	parts := strings.Fields(permission)
	if len(parts) == 2 {
		return strings.ToUpper(parts[0]), parts[1]
	}
	return "", permission
}

// IsAdmin checks if a user is an admin.
func (s *authService) IsAdmin(userID string) bool {
	value, exists := s.users.Load(userID)
	if !exists {
		return false
	}
	return value.(User).IsAdmin
}

// matchesPath performs simple path matching, supporting basic wildcards and path parameters.
func (s *authService) matchesPath(permission, path string) bool {
	// 1. Exact match
	if permission == path {
		return true
	}

	// 2. Handle wildcard ** (matches all sub-paths)
	if strings.Contains(permission, "**") {
		prefix := strings.Split(permission, "**")[0]
		return strings.HasPrefix(path, prefix)
	}

	// 3. Handle single-level wildcard * and path parameter :param
	return s.matchesSegments(permission, path)
}

// matchesSegments matches by path segments.
func (s *authService) matchesSegments(permission, path string) bool {
	permSegments := strings.Split(strings.Trim(permission, "/"), "/")
	pathSegments := strings.Split(strings.Trim(path, "/"), "/")

	// Segments must be the same length (unless there's a wildcard)
	if len(permSegments) != len(pathSegments) {
		return false
	}

	// Compare each segment
	for i, permSeg := range permSegments {
		pathSeg := pathSegments[i]

		if permSeg == pathSeg {
			continue
		}
		if strings.HasPrefix(permSeg, ":") {
			continue
		}
		if permSeg == "*" {
			continue
		}
		return false
	}

	return true
}

// NewAuthMiddleware returns a HandlerContextFunc that validates requests using the given authService.
func NewAuthMiddleware(svc *authService, pathSets ...[]string) HandlerContextFunc {
	var publicPaths, adminPaths []string
	if len(pathSets) > 0 {
		publicPaths = pathSets[0]
	}
	if len(pathSets) > 1 {
		adminPaths = pathSets[1]
	}
	return func(ctx *Context) {
		path := ctx.Request().URL.Path
		if matchesAnyPath(svc, publicPaths, path) {
			ctx.Next()
			return
		}

		userID := bearerToken(ctx.Request().Header.Get("Authorization"))
		if userID == "" {
			response.ErrorWithCode(ctx, http.StatusUnauthorized, response.ErrUnauthorized.Code, "missing bearer token")
			ctx.Abort()
			return
		}
		user, exists := svc.GetUserByID(userID)
		if !exists {
			response.ErrorWithCode(ctx, http.StatusUnauthorized, response.ErrUnauthorized.Code, "invalid bearer token")
			ctx.Abort()
			return
		}
		if matchesAnyPath(svc, adminPaths, path) && !user.IsAdmin {
			response.ErrorWithCode(ctx, http.StatusForbidden, response.ErrForbidden.Code, "administrator permission required")
			ctx.Abort()
			return
		}
		if err := svc.Validate(userID, ctx.Request().Method, path); err != nil {
			response.ErrorWithCode(ctx, http.StatusForbidden, response.ErrForbidden.Code, err.Error())
			ctx.Abort()
			return
		}
		ctx.UserID = userID
		ctx.IsAdmin = svc.IsAdmin(userID)
		ctx.Next()
	}
}

func bearerToken(header string) string {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func matchesAnyPath(svc *authService, patterns []string, path string) bool {
	for _, pattern := range patterns {
		if svc.matchesPath(pattern, path) {
			return true
		}
	}
	return false
}
