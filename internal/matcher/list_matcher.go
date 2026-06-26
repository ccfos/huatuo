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

package matcher

import (
	"fmt"
	"regexp"
)

// ListMatcher matches a value against a whitelist of full-match regex patterns.
// An empty ListMatcher matches nothing.
type ListMatcher struct {
	rules []*regexp.Regexp
}

// NewListMatcher compiles whitelist patterns. Each pattern is anchored so a
// literal device name such as "eth0" keeps the old exact-match behavior, while
// patterns such as "eth.*" can select a group of values.
func NewListMatcher(patterns []string) (*ListMatcher, error) {
	m := &ListMatcher{rules: make([]*regexp.Regexp, 0, len(patterns))}
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil {
			return nil, fmt.Errorf("invalid list pattern %q: %w", pattern, err)
		}
		m.rules = append(m.rules, re)
	}
	return m, nil
}

// Match reports whether value matches any whitelist pattern.
func (m *ListMatcher) Match(value string) bool {
	if m == nil {
		return false
	}
	for _, re := range m.rules {
		if re.MatchString(value) {
			return true
		}
	}
	return false
}

// Filter returns values that match the whitelist, preserving the input order.
func (m *ListMatcher) Filter(values []string) []string {
	var out []string
	for _, value := range values {
		if m.Match(value) {
			out = append(out, value)
		}
	}
	return out
}
