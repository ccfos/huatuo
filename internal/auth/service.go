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

// Package auth provides framework-independent authentication and authorization.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrMissingBearerToken indicates that a request has no usable bearer token.
	ErrMissingBearerToken = errors.New("missing bearer token")
	// ErrInvalidBearerToken indicates that a bearer token is not configured.
	ErrInvalidBearerToken = errors.New("invalid bearer token")
	// ErrPermissionDenied indicates that a principal cannot access a resource.
	ErrPermissionDenied = errors.New("permission denied")
	// ErrAdministratorRequired indicates that a resource is restricted to administrators.
	ErrAdministratorRequired = errors.New("administrator permission required")
)

// Permission grants access to an HTTP method and path pattern.
type Permission string

// Principal is the authenticated request identity.
type Principal struct {
	ID          string
	Permissions []Permission
	IsAdmin     bool
}

// UserConfig defines one statically configured bearer-token identity.
type UserConfig struct {
	ID          string
	BearerToken string
	Permissions []string
	IsAdmin     bool
}

// Service authenticates immutable, statically configured identities.
type Service struct {
	usersByToken map[string]Principal
}

// TokenAuthenticator authenticates bearer tokens without creating identities.
type TokenAuthenticator struct {
	tokenDigests [][sha256.Size]byte
}

// NewTokenAuthenticator snapshots the accepted bearer tokens.
func NewTokenAuthenticator(tokens []string) *TokenAuthenticator {
	tokenDigests := make([][sha256.Size]byte, len(tokens))
	for i := range tokens {
		tokenDigests[i] = sha256.Sum256([]byte(tokens[i]))
	}
	return &TokenAuthenticator{tokenDigests: tokenDigests}
}

// AuthenticateBearer validates an HTTP Authorization header.
func (a *TokenAuthenticator) AuthenticateBearer(header string) error {
	token, err := ParseBearerToken(header)
	if err != nil {
		return err
	}
	tokenDigest := sha256.Sum256([]byte(token))
	matched := 0
	for i := range a.tokenDigests {
		matched |= subtle.ConstantTimeCompare(tokenDigest[:], a.tokenDigests[i][:])
	}
	if matched == 0 {
		return ErrInvalidBearerToken
	}
	return nil
}

// NewService constructs an authentication service from static user configuration.
func NewService(users []UserConfig) *Service {
	usersByToken := make(map[string]Principal, len(users))
	for _, user := range users {
		permissions := make([]Permission, len(user.Permissions))
		for i := range user.Permissions {
			permissions[i] = Permission(user.Permissions[i])
		}
		usersByToken[user.BearerToken] = Principal{
			ID:          user.ID,
			Permissions: permissions,
			IsAdmin:     user.IsAdmin,
		}
	}
	return &Service{usersByToken: usersByToken}
}

// Authenticate returns the principal associated with a raw bearer token.
func (s *Service) Authenticate(token string) (Principal, bool) {
	principal, ok := s.usersByToken[token]
	if !ok {
		return Principal{}, false
	}
	return clonePrincipal(principal), true
}

// AuthenticateBearer authenticates an HTTP Authorization header.
func (s *Service) AuthenticateBearer(header string) (Principal, error) {
	token, err := ParseBearerToken(header)
	if err != nil {
		return Principal{}, err
	}
	principal, ok := s.Authenticate(token)
	if !ok {
		return Principal{}, ErrInvalidBearerToken
	}
	return principal, nil
}

// Authorize checks whether a principal may access an HTTP method and path.
func (s *Service) Authorize(principal Principal, method, path string) error {
	if principal.IsAdmin {
		return nil
	}

	for _, permission := range principal.Permissions {
		permissionMethod, permissionPath := splitPermission(string(permission))
		if (permissionMethod == "" || permissionMethod == method) &&
			MatchesPath(permissionPath, path) {
			return nil
		}
	}

	return fmt.Errorf(
		"%w: user %q does not have permission to access %s %s",
		ErrPermissionDenied,
		principal.ID,
		method,
		path,
	)
}

// RequireAdministrator checks whether a principal is an administrator.
func RequireAdministrator(principal Principal) error {
	if principal.IsAdmin {
		return nil
	}
	return ErrAdministratorRequired
}

// MatchesAnyPath reports whether a path matches one of the configured patterns.
func MatchesAnyPath(patterns []string, path string) bool {
	for _, pattern := range patterns {
		if MatchesPath(pattern, path) {
			return true
		}
	}
	return false
}

// MatchesPath matches complete path segments, including *, **, and :parameter.
// It evaluates an iterative DP table instead of map-memoized recursion, so a
// per-request authorization does not allocate two maps and a closure per call.
func MatchesPath(permission, path string) bool {
	if permission == path {
		return true
	}

	permissionSegments := strings.Split(strings.Trim(permission, "/"), "/")
	pathSegments := strings.Split(strings.Trim(path, "/"), "/")

	permLen, pathLen := len(permissionSegments), len(pathSegments)
	// next[i] = dp[p+1][i], current[i] = dp[p][i]; dp[p][i] reports whether
	// permissionSegments[p:] matches pathSegments[i:].
	next := make([]bool, pathLen+1)
	current := make([]bool, pathLen+1)

	// dp[permLen][i] = (i == pathLen)
	next[pathLen] = true

	for p := permLen - 1; p >= 0; p-- {
		segment := permissionSegments[p]
		for i := pathLen; i >= 0; i-- {
			var matched bool
			switch {
			case segment == "**":
				// Wildcards consume a descendant segment so collection
				// permissions remain explicit.
				matched = i < pathLen && (next[i+1] || current[i+1])
			case i < pathLen:
				matched = (segment == pathSegments[i] ||
					segment == "*" ||
					strings.HasPrefix(segment, ":")) && next[i+1]
			}
			current[i] = matched
		}
		next, current = current, next
	}

	return next[0]
}

// ParseBearerToken extracts a token from an HTTP Authorization header.
func ParseBearerToken(header string) (string, error) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", ErrMissingBearerToken
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", ErrMissingBearerToken
	}
	return token, nil
}

func splitPermission(permission string) (string, string) {
	parts := strings.Fields(permission)
	if len(parts) == 2 {
		return strings.ToUpper(parts[0]), parts[1]
	}
	return "", permission
}

func clonePrincipal(principal Principal) Principal {
	principal.Permissions = append([]Permission(nil), principal.Permissions...)
	return principal
}
