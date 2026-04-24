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
	"strings"
	"sync"
)

type Permission string

type User struct {
	ID          string
	Name        string
	Permissions []Permission
	IsAdmin     bool
}

type UserConfig struct {
	ID          string
	Name        string
	Permissions []string
	IsAdmin     bool
}

type authService struct {
	users sync.Map
}

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

func (s *authService) Add(user User) {
	s.users.Store(user.ID, user)
}

func (s *authService) Delete(userID string) {
	s.users.Delete(userID)
}

func (s *authService) GetUserById(userID string) (User, bool) {
	value, exists := s.users.Load(userID)
	if !exists {
		return User{}, false
	}
	return value.(User), true
}

func (s *authService) Validate(userID, path string) error {
	value, exists := s.users.Load(userID)
	if !exists {
		return fmt.Errorf("user %s not found", userID)
	}

	user := value.(User)
	if user.IsAdmin {
		return nil
	}

	for _, perm := range user.Permissions {
		if s.matchesPath(string(perm), path) {
			return nil
		}
	}

	return fmt.Errorf("user %s does not have permission to access %s", userID, path)
}

func (s *authService) IsAdmin(userID string) bool {
	value, exists := s.users.Load(userID)
	if !exists {
		return false
	}
	return value.(User).IsAdmin
}

func (s *authService) matchesPath(permission, path string) bool {
	if permission == path {
		return true
	}

	if strings.Contains(permission, "**") {
		prefix := strings.Split(permission, "**")[0]
		return strings.HasPrefix(path, prefix)
	}

	return s.matchesSegments(permission, path)
}

func (s *authService) matchesSegments(permission, path string) bool {
	permSegments := strings.Split(strings.Trim(permission, "/"), "/")
	pathSegments := strings.Split(strings.Trim(path, "/"), "/")
	if len(permSegments) != len(pathSegments) {
		return false
	}

	for i, permSeg := range permSegments {
		pathSeg := pathSegments[i]
		if permSeg == pathSeg || strings.HasPrefix(permSeg, ":") || permSeg == "*" {
			continue
		}
		return false
	}

	return true
}

func NewAuthMiddleware(svc *authService) HandlerContextFunc {
	return func(ctx *Context) {
		userID := ctx.Request().Header.Get("Authorization")
		if userID == "" {
			ctx.JSON(http.StatusUnauthorized, map[string]any{"code": 401, "message": "missing user ID"})
			ctx.Abort()
			return
		}
		if err := svc.Validate(userID, ctx.Request().URL.Path); err != nil {
			ctx.JSON(http.StatusForbidden, map[string]any{"code": 403, "message": err.Error()})
			ctx.Abort()
			return
		}
		ctx.UserID = userID
		ctx.IsAdmin = svc.IsAdmin(userID)
		ctx.Next()
	}
}
