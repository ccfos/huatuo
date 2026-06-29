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

// FieldSpec describes a named field to match against.
// Pattern is a regex string; an empty Pattern causes the field to be skipped.
// Extract pulls the field value from a value of type T.
type FieldSpec[T any] struct {
	Name    string
	Pattern string
	Extract func(T) string
}

type fieldRule[T any] struct {
	re      *regexp.Regexp
	extract func(T) string
}

func (fr *fieldRule[T]) match(v T) bool {
	return fr.re.MatchString(fr.extract(v))
}

// FieldMatcher applies a set of AND-ed regex rules to named fields of T.
// An empty FieldMatcher (no rules) matches every value.
type FieldMatcher[T any] struct {
	rules []*fieldRule[T]
}

// NewFieldMatcher compiles all non-empty patterns from specs and returns a ready-to-use
// FieldMatcher. Returns an error if any pattern is not a valid regular expression.
func NewFieldMatcher[T any](specs []FieldSpec[T]) (*FieldMatcher[T], error) {
	rules := make([]*fieldRule[T], 0, len(specs))
	for _, s := range specs {
		if s.Pattern == "" {
			continue
		}
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q for field %q: %w", s.Pattern, s.Name, err)
		}
		rules = append(rules, &fieldRule[T]{
			re:      re,
			extract: s.Extract,
		})
	}
	return &FieldMatcher[T]{rules: rules}, nil
}

// Match returns true only if every compiled rule matches v.
// Returns true when the FieldMatcher has no rules (match-all semantics).
func (fm *FieldMatcher[T]) Match(v T) bool {
	for _, r := range fm.rules {
		if !r.match(v) {
			return false
		}
	}
	return true
}

// MatchAny returns true if any compiled rule matches v (OR semantics).
// Returns false when the FieldMatcher has no rules.
func (fm *FieldMatcher[T]) MatchAny(v T) bool {
	for _, r := range fm.rules {
		if r.match(v) {
			return true
		}
	}
	return false
}
