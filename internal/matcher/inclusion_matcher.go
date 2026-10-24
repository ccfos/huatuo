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

// inclusionMatcher is the shared generic base for ValueMatcher and ContainerMatcher.
// It holds pre-compiled include/exclude FieldMatchers and provides the core Match logic.
type inclusionMatcher[T any] struct {
	include *FieldMatcher[T]
	exclude *FieldMatcher[T]
}

// newInclusionMatcher compiles include and exclude FieldSpec slices into a ready-to-use
// inclusionMatcher. Returns an error if any pattern is invalid.
func newInclusionMatcher[T any](include, exclude []FieldSpec[T]) (*inclusionMatcher[T], error) {
	im := &inclusionMatcher[T]{}

	if len(include) > 0 {
		fm, err := NewFieldMatcher(include)
		if err != nil {
			return nil, err
		}
		im.include = fm
	}

	if len(exclude) > 0 {
		fm, err := NewFieldMatcher(exclude)
		if err != nil {
			return nil, err
		}
		im.exclude = fm
	}

	return im, nil
}

// match returns true when v passes the filter: present in the include set
// (when one is configured) and absent from the exclude set.
func (im *inclusionMatcher[T]) match(v T) bool {
	if im.include != nil && !im.include.Match(v) {
		return false
	}
	if im.exclude != nil && im.exclude.MatchAny(v) {
		return false
	}
	return true
}
