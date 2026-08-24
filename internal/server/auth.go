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
	"errors"

	authn "huatuo-bamai/internal/auth"
	"huatuo-bamai/internal/server/response"
)

type (
	Permission = authn.Permission
	User       = authn.Principal
	UserConfig = authn.UserConfig
)

type authService struct {
	service *authn.Service
}

// NewAuthService creates a compatibility adapter for the HTTP server.
func NewAuthService(users []UserConfig) *authService {
	return &authService{service: authn.NewService(users)}
}

// Authenticate returns the principal associated with a bearer token.
func (s *authService) Authenticate(token string) (User, bool) {
	return s.service.Authenticate(token)
}

// Validate validates if a user has access to a specific path.
func (s *authService) Validate(user User, request ...string) error {
	method, path := "", ""
	if len(request) == 1 {
		path = request[0]
	} else if len(request) >= 2 {
		method, path = request[0], request[1]
	}
	return s.service.Authorize(user, method, path)
}

// matchesPath performs simple path matching, supporting basic wildcards and path parameters.
func (s *authService) matchesPath(permission, path string) bool {
	return authn.MatchesPath(permission, path)
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

		user, err := svc.service.AuthenticateBearer(ctx.Request().Header.Get("Authorization"))
		if err != nil {
			ctx.Header("WWW-Authenticate", "Bearer")
			message := authn.ErrInvalidBearerToken.Error()
			if errors.Is(err, authn.ErrMissingBearerToken) {
				message = authn.ErrMissingBearerToken.Error()
			}
			response.ErrorWithCode(
				ctx,
				ctx.ErrorStatusMapper(),
				response.ErrUnauthorized.Code,
				message,
			)
			ctx.Abort()
			return
		}
		if matchesAnyPath(svc, adminPaths, path) && !user.IsAdmin {
			response.ErrorWithCode(
				ctx,
				ctx.ErrorStatusMapper(),
				response.ErrForbidden.Code,
				authn.ErrAdministratorRequired.Error(),
			)
			ctx.Abort()
			return
		}
		if err := svc.Validate(user, ctx.Request().Method, path); err != nil {
			response.ErrorWithCode(
				ctx,
				ctx.ErrorStatusMapper(),
				response.ErrForbidden.Code,
				err.Error(),
			)
			ctx.Abort()
			return
		}
		ctx.UserID = user.ID
		ctx.IsAdmin = user.IsAdmin
		ctx.c.Request = ctx.c.Request.WithContext(authn.WithPrincipal(ctx.Request().Context(), user))
		ctx.Next()
	}
}

func matchesAnyPath(_ *authService, patterns []string, path string) bool {
	return authn.MatchesAnyPath(patterns, path)
}
