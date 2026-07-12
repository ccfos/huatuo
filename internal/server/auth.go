// Copyright 2025 The HuaTuo Authors
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
	"sort"
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

// UserManager exposes the operations the console uses to administer the
// in-memory user/API-key registry. Methods are safe for concurrent use.
type UserManager interface {
	ListUsers() []User
	Add(user User)
	Delete(userID string)
	GetUserById(userID string) (User, bool)
	Validate(userID, path string) error
	IsAdmin(userID string) bool
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

// ListUsers returns all registered users in a stable, id-sorted order.
func (s *authService) ListUsers() []User {
	users := make([]User, 0)
	s.users.Range(func(_, value any) bool {
		users = append(users, value.(User))
		return true
	})
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	return users
}

// GetUserById gets a user by ID.
func (s *authService) GetUserById(userID string) (User, bool) {
	value, exists := s.users.Load(userID)
	if !exists {
		return User{}, false
	}
	return value.(User), true
}

// Validate validates if a user has access to a specific path.
func (s *authService) Validate(userID, path string) error {
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
		if s.matchesPath(string(perm), path) {
			return nil
		}
	}

	return fmt.Errorf("user %s does not have permission to access %s", userID, path)
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
// Requests whose path matches one of publicPaths (exact match or prefix + "/")
// bypass authentication so that public assets such as the web console can be
// served without credentials.
func NewAuthMiddleware(svc *authService, publicPaths []string) HandlerContextFunc {
	return func(ctx *Context) {
		if isPublicPath(ctx.Request().URL.Path, publicPaths) {
			ctx.Next()
			return
		}
		userID := ctx.Request().Header.Get("Authorization")
		if userID == "" {
			response.ErrorWithCode(ctx, http.StatusUnauthorized, response.ErrUnauthorized.Code, "missing user ID")
			ctx.Abort()
			return
		}
		if err := svc.Validate(userID, ctx.Request().URL.Path); err != nil {
			response.ErrorWithCode(ctx, http.StatusForbidden, response.ErrForbidden.Code, err.Error())
			ctx.Abort()
			return
		}
		ctx.UserID = userID
		ctx.IsAdmin = svc.IsAdmin(userID)
		ctx.Next()
	}
}

// isPublicPath reports whether path is exempt from authentication. A path is
// public when it equals a configured prefix exactly or starts with prefix+"/".
func isPublicPath(path string, publicPaths []string) bool {
	for _, prefix := range publicPaths {
		if prefix == "" {
			continue
		}
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
