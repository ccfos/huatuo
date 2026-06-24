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
	"strings"
)

// ListMatcher matches a value when any configured list entry matches it.
// Entries without explicit regex operators are matched exactly. Entries
// containing regex operators are treated as full regular expressions.
type ListMatcher struct {
	exact map[string]struct{}
	rules []*regexp.Regexp
}

// NewListMatcher compiles list entries into a ListMatcher.
// An empty list matches nothing.
func NewListMatcher(patterns []string) (*ListMatcher, error) {
	lm := &ListMatcher{
		exact: make(map[string]struct{}),
		rules: make([]*regexp.Regexp, 0, len(patterns)),
	}
	for _, pattern := range patterns {
		if !hasRegexOperator(pattern) {
			lm.exact[pattern] = struct{}{}
			continue
		}

		re, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil {
			return nil, fmt.Errorf("invalid list pattern %q: %w", pattern, err)
		}
		lm.rules = append(lm.rules, re)
	}
	return lm, nil
}

func hasRegexOperator(pattern string) bool {
	// Treat a plain dot as a literal interface-name character so legacy VLAN
	// names such as "eth0.100" keep exact-match behavior.
	return strings.ContainsAny(pattern, `\+*?()|[]{}^$`)
}

// Match reports whether value matches any compiled list entry.
func (lm *ListMatcher) Match(value string) bool {
	if lm == nil {
		return false
	}
	if _, ok := lm.exact[value]; ok {
		return true
	}
	for _, rule := range lm.rules {
		if rule.MatchString(value) {
			return true
		}
	}
	return false
}
