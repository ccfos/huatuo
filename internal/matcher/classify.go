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

package matcher

import (
	"fmt"
	"regexp"
)

// ValidateClassifications validates the name and regular expression in every
// classification rule.
func ValidateClassifications(patternList [][]string) error {
	for i, pattern := range patternList {
		if len(pattern) != 2 {
			return fmt.Errorf("rule %d must contain a name and regular expression", i)
		}
		if pattern[0] == "" {
			return fmt.Errorf("rule %d has an empty name", i)
		}
		if _, err := regexp.Compile(pattern[1]); err != nil {
			return fmt.Errorf("rule %d %q has invalid regular expression %q: %w",
				i, pattern[0], pattern[1], err)
		}
	}
	return nil
}

func Classify(patternList [][]string, value string) (string, bool) {
	for _, p := range patternList {
		if len(p) != 2 {
			return "none", false
		}

		reg, err := regexp.Compile(p[1])
		if err != nil {
			return "none", false
		}
		if reg.MatchString(value) {
			return p[0], true
		}
	}

	return "none", false
}
