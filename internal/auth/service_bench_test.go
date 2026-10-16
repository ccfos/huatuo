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
	"strings"
	"testing"
)

// matchesPathReference is the original map-memoized recursive matcher, kept
// as an oracle to prove the iterative DP is behaviorally identical.
func matchesPathReference(permission, path string) bool {
	if permission == path {
		return true
	}

	permissionSegments := splitSegments(permission)
	pathSegments := splitSegments(path)
	memo := make(map[[2]int]bool)
	visited := make(map[[2]int]bool)

	var match func(int, int) bool
	match = func(permissionIndex, pathIndex int) bool {
		position := [2]int{permissionIndex, pathIndex}
		if visited[position] {
			return memo[position]
		}
		visited[position] = true

		matched := false
		switch {
		case permissionIndex == len(permissionSegments):
			matched = pathIndex == len(pathSegments)
		case permissionSegments[permissionIndex] == "**":
			matched = pathIndex < len(pathSegments) &&
				(match(permissionIndex+1, pathIndex+1) ||
					match(permissionIndex, pathIndex+1))
		case pathIndex < len(pathSegments):
			permissionSegment := permissionSegments[permissionIndex]
			matched = (permissionSegment == pathSegments[pathIndex] ||
				permissionSegment == "*" ||
				segHasParamPrefix(permissionSegment)) &&
				match(permissionIndex+1, pathIndex+1)
		}

		memo[position] = matched
		return matched
	}

	return match(0, 0)
}

func splitSegments(s string) []string {
	// Mirrors strings.Split(strings.Trim(s, "/"), "/") exactly, including
	// empty-segment results for leading/trailing/duplicate slashes.
	trimmed := strings.Trim(s, "/")
	out := []string{}
	current := ""
	for _, r := range trimmed {
		if r == '/' {
			out = append(out, current)
			current = ""
			continue
		}
		current += string(r)
	}
	return append(out, current)
}

func segHasParamPrefix(s string) bool {
	return len(s) > 0 && s[0] == ':'
}

func TestMatchesPathMatchesReference(t *testing.T) {
	permissions := []string{
		"/", "", "/api", "/api/*", "/api/**", "/api/v1/containers",
		"/api/v1/:id", "/api/v1/**/tail", "GET /api/v1/x", "/a/**/b",
		"/**", "*", "/api/v1/**",
	}
	paths := []string{
		"/", "", "/api", "/api/v1", "/api/v1/containers", "/api/v1/containers/42",
		"/api/v2", "/x/y/z", "/a/x/y/b", "/api/v1/containers/tail",
	}

	for _, permission := range permissions {
		for _, path := range paths {
			got := MatchesPath(permission, path)
			want := matchesPathReference(permission, path)
			if got != want {
				t.Errorf("MatchesPath(%q, %q) = %v, reference = %v",
					permission, path, got, want)
			}
		}
	}
}

func BenchmarkMatchesPath(b *testing.B) {
	const permission = "/api/v1/containers/**"
	const path = "/api/v1/containers/7f3a/logs/tail"

	b.ReportAllocs()
	for b.Loop() {
		if !MatchesPath(permission, path) {
			b.Fatal("expected match")
		}
	}
}
