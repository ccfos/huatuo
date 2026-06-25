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

type valueListRule struct {
	re *regexp.Regexp
}

// ValueListMatcher matches a string against any configured regex pattern.
// A pattern must match the complete value, so "eth0" keeps exact-list behavior
// while "eth.*" can be used to match multiple device names.
type ValueListMatcher struct {
	rules []valueListRule
}

func NewValueListMatcher(patterns []string) (*ValueListMatcher, error) {
	rules := make([]valueListRule, 0, len(patterns))
	for i, pattern := range patterns {
		if pattern == "" {
			continue
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern at index %d %q: %w", i, pattern, err)
		}

		rules = append(rules, valueListRule{re: re})
	}

	return &ValueListMatcher{rules: rules}, nil
}

func (m *ValueListMatcher) Match(value string) bool {
	if m == nil {
		return false
	}

	for _, rule := range m.rules {
		match := rule.re.FindStringIndex(value)
		if match != nil && match[0] == 0 && match[1] == len(value) {
			return true
		}
	}
	return false
}
